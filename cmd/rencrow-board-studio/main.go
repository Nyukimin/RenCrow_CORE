package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

func loopback(raw string) bool {
	host, _, err := net.SplitHostPort(raw)
	ip := net.ParseIP(host)
	return err == nil && ip != nil && ip.IsLoopback()
}

func sourceURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || !loopback(u.Host) || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("CORE Viewer must be a numeric loopback HTTP origin")
	}
	return u, nil
}

func readKey(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 256 {
		return nil, errors.New("invalid Studio token file")
	}
	if err := checkTokenPermissions(info.Mode()); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("Studio token file is unreadable")
	}
	key := []byte(strings.TrimSpace(string(raw)))
	if len(key) < 32 || strings.ContainsAny(string(key), "\r\n\t ") {
		return nil, errors.New("invalid Studio token")
	}
	return key, nil
}

type source struct {
	viewer *url.URL
	client *http.Client
}

func (s source) get(ctx context.Context, path string, dst any) error {
	target, err := s.viewer.Parse(path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	response, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("CORE Viewer returned %d for %s", response.StatusCode, target.Path)
	}
	const maxBytes = 12 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil || len(data) > maxBytes {
		return errors.New("could not read CORE Viewer response")
	}
	return json.Unmarshal(data, dst)
}

func handler(src source, key []byte) http.Handler {
	mux := http.NewServeMux()
	secret := sha256.Sum256([]byte("Bearer " + string(key)))
	mux.HandleFunc("GET /snapshot", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !loopback(r.RemoteAddr) || !loopback(r.Host) || r.Header.Get("Origin") != "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		presented := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if len(r.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare(presented[:], secret[:]) != 1 {
			http.Error(w, "authorization required", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		snapshot, ok := src.snapshot(ctx)
		if !ok {
			http.Error(w, "CORE Viewer unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(snapshot)
	})
	return mux
}

func (s source) snapshot(ctx context.Context) (studioSnapshot, bool) {
	var agents viewerAgents
	var tasks viewerTasks
	var work viewerWorkstreams
	var atlas viewerAtlas
	requests := []struct {
		path   string
		target any
	}{
		{"/viewer/agents", &agents},
		{"/viewer/tasks", &tasks},
		{"/viewer/workstreams?limit=100", &work},
		{"/viewer/atlas", &atlas},
	}
	errs := make([]error, len(requests))
	var group sync.WaitGroup
	for i, req := range requests {
		group.Add(1)
		go func() {
			defer group.Done()
			errs[i] = s.get(ctx, req.path, req.target)
		}()
	}
	group.Wait()
	agentsOK := errs[0] == nil && agents.Agents != nil
	tasksOK := errs[1] == nil && tasks.Items != nil
	workOK := errs[2] == nil && work.Workstreams != nil && work.Goals != nil && work.Artifacts != nil
	atlasOK := errs[3] == nil && atlas.Pipeline != nil && atlas.Backlog != nil
	if !agentsOK && !tasksOK && !workOK && !atlasOK {
		return studioSnapshot{}, false
	}
	if !agentsOK {
		agents = viewerAgents{}
	}
	if !tasksOK {
		tasks = viewerTasks{}
	}
	if !workOK {
		work = viewerWorkstreams{}
	}
	if !atlasOK {
		atlas = viewerAtlas{}
	}
	observed := time.Now().UTC()
	snapshot := projectStudio(observed, agents, tasks, work, atlas)
	snapshot.SourceHealth = map[string]string{"CORE": "ready", "agents": health(agentsOK), "task_stack": health(tasksOK), "projects": health(workOK),
		"work_items": health(workOK && tasksOK && atlasOK), "artifacts": health(workOK), "blockers": health(tasksOK && atlasOK),
		"activities": health(workOK && tasksOK), "atlas": health(atlasOK)}
	if !workOK || !tasksOK || !atlasOK {
		for i := range snapshot.Projects {
			snapshot.Projects[i].NotDone = nil
			snapshot.Projects[i].Blockers = nil
		}
	}
	return snapshot, true
}

func health(ok bool) string {
	if ok {
		return "ready"
	}
	return "unavailable"
}

func run() error {
	listen := flag.String("listen", "127.0.0.1:18804", "loopback Studio listener")
	viewer := flag.String("core", "http://127.0.0.1:18790", "CORE Viewer loopback origin")
	tokenFile := flag.String("token-file", "", "protected dedicated Studio read token")
	flag.Parse()
	if !loopback(*listen) {
		return errors.New("Studio listener must use loopback")
	}
	origin, err := sourceURL(*viewer)
	if err != nil {
		return err
	}
	key, err := readKey(*tokenFile)
	if err != nil {
		return err
	}
	src := source{viewer: origin, client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{MaxIdleConnsPerHost: 4, IdleConnTimeout: 45 * time.Second}}}
	server := &http.Server{Addr: *listen, Handler: handler(src, key), ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout: 5 * time.Second, WriteTimeout: 7 * time.Second, IdleTimeout: 45 * time.Second}
	return server.ListenAndServe()
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
