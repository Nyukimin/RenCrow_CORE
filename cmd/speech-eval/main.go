package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tts"
	"github.com/Nyukimin/RenCrow_CORE/internal/speecheval"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type cliOptions struct {
	gatewayURL              string
	tokenFile               string
	tokenEnv                string
	outputDir               string
	answerFile              string
	expectedProfileRevision int
	requestTimeout          time.Duration
}

const (
	defaultRequestTimeout = 300 * time.Second
	maxRequestTimeout     = 5 * time.Minute
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, os.LookupEnv); err != nil {
		fmt.Fprintf(os.Stderr, "speech-eval: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, lookupEnv func(string) (string, bool)) error {
	return runWithVariantOrder(ctx, args, stdout, stderr, lookupEnv, randomVariantOrder)
}

// runWithVariantOrder keeps the production chooser cryptographically random
// while allowing end-to-end tests to exercise both private A/B assignments.
func runWithVariantOrder(ctx context.Context, args []string, stdout, stderr io.Writer, lookupEnv func(string) (string, bool), chooseAExpressive func() (bool, error)) error {
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	if chooseAExpressive == nil {
		return fmt.Errorf("variant order chooser is required")
	}
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	opts, err := parseOptions(args, stderr)
	if err != nil {
		return err
	}
	if err := validateGatewayURL(opts.gatewayURL); err != nil {
		return err
	}
	token, err := resolveGatewayToken(opts.tokenFile, opts.tokenEnv, lookupEnv)
	if err != nil {
		return err
	}
	if err := speecheval.ValidateCases(speecheval.AllCases()); err != nil {
		return fmt.Errorf("fixed speech evaluation cases are invalid: %w", err)
	}
	outputRoot, answerPath, err := validateOutputPaths(opts.outputDir, opts.answerFile)
	if err != nil {
		return err
	}
	evalRoot, err := ensureSpeechEvalRoot(outputRoot)
	if err != nil {
		return err
	}

	setID, setDir, err := createSetDirectory(evalRoot)
	if err != nil {
		return err
	}
	published := false
	answerCreated := false
	defer func() {
		if !published {
			_ = os.RemoveAll(setDir)
			if answerCreated {
				_ = os.Remove(answerPath)
			}
		}
	}()

	bridge := tts.NewRenCrowTTSBridge(tts.RenCrowTTSBridgeConfig{
		HTTPBaseURL:    opts.gatewayURL,
		AuthToken:      token,
		RequestTimeout: opts.requestTimeout,
	})
	sessionID := string(modulecore.NewSessionID())
	items := speecheval.AllCases()
	rendered := make([]speecheval.RenderedCase, 0, len(items))
	for _, item := range items {
		turnID := string(modulecore.NewTurnID())
		pair := speecheval.BuildPair(item, sessionID, turnID)
		aExpressive, err := chooseAExpressive()
		if err != nil {
			return fmt.Errorf("choose blinded variant order for case %s: %w", item.ID, err)
		}
		aVariant, bVariant := "neutral", "expressive"
		planA, planB := pair.NeutralPlan, pair.ExpressivePlan
		if aExpressive {
			aVariant, bVariant = bVariant, aVariant
			planA, planB = pair.ExpressivePlan, pair.NeutralPlan
		}
		audioA, err := synthesizeVariant(ctx, bridge, opts.expectedProfileRevision, token, pair.SessionID, pair.TurnID, planA, item.ID, "A")
		if err != nil {
			return err
		}
		audioB, err := synthesizeVariant(ctx, bridge, opts.expectedProfileRevision, token, pair.SessionID, pair.TurnID, planB, item.ID, "B")
		if err != nil {
			return err
		}
		if audioA.IrodoriSeed == nil || audioB.IrodoriSeed == nil || *audioA.IrodoriSeed != *audioB.IrodoriSeed {
			return fmt.Errorf("case %s A/B Gateway seeds do not match", item.ID)
		}
		filenameA, wavPathA, err := createOpaqueWAVPath(setDir)
		if err != nil {
			return fmt.Errorf("case %s A output path: %w", item.ID, err)
		}
		if err := writeExclusiveFile(wavPathA, audioA.WAV, 0o644); err != nil {
			return fmt.Errorf("write case %s A WAV: %w", item.ID, err)
		}
		filenameB, wavPathB, err := createOpaqueWAVPath(setDir)
		if err != nil {
			return fmt.Errorf("case %s B output path: %w", item.ID, err)
		}
		if err := writeExclusiveFile(wavPathB, audioB.WAV, 0o644); err != nil {
			return fmt.Errorf("write case %s B WAV: %w", item.ID, err)
		}
		rendered = append(rendered, speecheval.RenderedCase{
			Case:      item,
			SessionID: pair.SessionID,
			TurnID:    pair.TurnID,
			AVariant:  aVariant,
			BVariant:  bVariant,
			A:         artifactFor(filenameA, audioA),
			B:         artifactFor(filenameB, audioB),
		})
	}

	manifest, answer, err := speecheval.BuildPack(setID, opts.expectedProfileRevision, rendered)
	if err != nil {
		return fmt.Errorf("build speech evaluation pack: %w", err)
	}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode public speech evaluation manifest: %w", err)
	}
	answerJSON, err := json.MarshalIndent(answer, "", "  ")
	if err != nil {
		return fmt.Errorf("encode private speech evaluation answer: %w", err)
	}
	answerJSON = append(answerJSON, '\n')
	if err := writeExclusiveFile(answerPath, answerJSON, 0o600); err != nil {
		return fmt.Errorf("write private speech evaluation answer: %w", err)
	}
	answerCreated = true
	manifestPath := filepath.Join(evalRoot, "manifest.json")
	if err := writeAtomicManifest(manifestPath, append(manifestJSON, '\n')); err != nil {
		return fmt.Errorf("publish public speech evaluation manifest: %w", err)
	}
	published = true
	if _, err := fmt.Fprintf(stdout, "speech evaluation ready: %s\n", manifestPath); err != nil {
		return fmt.Errorf("write speech evaluation status: %w", err)
	}
	return nil
}

func parseOptions(args []string, stderr io.Writer) (cliOptions, error) {
	var opts cliOptions
	flags := flag.NewFlagSet("speech-eval", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.gatewayURL, "gateway-url", "", "RenCrow_TTS Gateway base URL")
	flags.StringVar(&opts.tokenFile, "token-file", "", "owner-only TTS Gateway token file")
	flags.StringVar(&opts.tokenEnv, "token-env", "RENCROW_TTS_TOKEN", "environment variable containing the TTS Gateway token")
	flags.StringVar(&opts.outputDir, "output-dir", "", "absolute configured TTS audio output directory")
	flags.StringVar(&opts.answerFile, "answer-file", "", "absolute private answer JSON path outside output-dir")
	flags.IntVar(&opts.expectedProfileRevision, "expected-profile-revision", 0, "approved live Mio voice profile revision")
	flags.DurationVar(&opts.requestTimeout, "request-timeout", defaultRequestTimeout, "bounded TTS Gateway request timeout (maximum 5m)")
	if err := flags.Parse(args); err != nil {
		return cliOptions{}, err
	}
	if flags.NArg() != 0 {
		return cliOptions{}, fmt.Errorf("unexpected positional arguments")
	}
	if strings.TrimSpace(opts.gatewayURL) == "" {
		return cliOptions{}, fmt.Errorf("--gateway-url is required")
	}
	if strings.TrimSpace(opts.tokenFile) == "" && strings.TrimSpace(opts.tokenEnv) == "" {
		return cliOptions{}, fmt.Errorf("--token-file or --token-env is required")
	}
	if strings.TrimSpace(opts.outputDir) == "" || strings.TrimSpace(opts.answerFile) == "" {
		return cliOptions{}, fmt.Errorf("--output-dir and --answer-file are required")
	}
	if opts.expectedProfileRevision <= 0 {
		return cliOptions{}, fmt.Errorf("--expected-profile-revision must be positive")
	}
	if opts.requestTimeout <= 0 || opts.requestTimeout > maxRequestTimeout {
		return cliOptions{}, fmt.Errorf("--request-timeout must be positive and no greater than 5m")
	}
	return opts, nil
}

func validateGatewayURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || !parsed.IsAbs() || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("--gateway-url must be an absolute HTTP(S) Gateway base URL without userinfo, query, or fragment")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("--gateway-url must use HTTP or HTTPS")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return fmt.Errorf("--gateway-url must be the Gateway base URL, without a route path")
	}
	return nil
}

func resolveGatewayToken(tokenFile, tokenEnv string, lookupEnv func(string) (string, bool)) (string, error) {
	if strings.TrimSpace(tokenFile) != "" {
		return readOwnerTokenFile(tokenFile)
	}
	if strings.TrimSpace(tokenEnv) == "" || lookupEnv == nil {
		return "", fmt.Errorf("TTS Gateway token is required through --token-file or --token-env")
	}
	value, ok := lookupEnv(strings.TrimSpace(tokenEnv))
	token := strings.TrimSpace(value)
	if !ok || token == "" || strings.ContainsAny(token, "\r\n") {
		return "", fmt.Errorf("TTS Gateway token environment variable is empty or invalid")
	}
	return token, nil
}

func readOwnerTokenFile(path string) (string, error) {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("TTS owner token file path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect TTS owner token file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("TTS owner token file must be a regular non-symlink file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("TTS owner token file must be owner-only")
	}
	if info.Size() <= 0 || info.Size() > 64<<10 {
		return "", fmt.Errorf("TTS owner token file must contain one bounded token")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open TTS owner token file: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat TTS owner token file: %w", err)
	}
	if !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() || (runtime.GOOS != "windows" && openedInfo.Mode().Perm()&0o077 != 0) {
		return "", fmt.Errorf("TTS owner token file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return "", fmt.Errorf("read TTS owner token file: %w", err)
	}
	if len(data) > 64<<10 {
		return "", fmt.Errorf("TTS owner token exceeds bounded size")
	}
	token := strings.TrimSpace(string(data))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return "", fmt.Errorf("TTS owner token file must contain one non-empty line")
	}
	return token, nil
}

func validateOutputPaths(outputDir, answerFile string) (string, string, error) {
	if !filepath.IsAbs(outputDir) || !filepath.IsAbs(answerFile) {
		return "", "", fmt.Errorf("--output-dir and --answer-file must be absolute paths")
	}
	outputAbs, err := filepath.Abs(outputDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve --output-dir: %w", err)
	}
	outputInfo, err := os.Stat(outputAbs)
	if err != nil || !outputInfo.IsDir() {
		return "", "", fmt.Errorf("--output-dir must be an existing directory")
	}
	outputReal, err := filepath.EvalSymlinks(outputAbs)
	if err != nil {
		return "", "", fmt.Errorf("resolve --output-dir symlinks: %w", err)
	}
	answerAbs, err := filepath.Abs(answerFile)
	if err != nil {
		return "", "", fmt.Errorf("resolve --answer-file: %w", err)
	}
	answerParent, err := filepath.EvalSymlinks(filepath.Dir(answerAbs))
	if err != nil {
		return "", "", fmt.Errorf("--answer-file parent must already exist: %w", err)
	}
	answerReal := filepath.Join(answerParent, filepath.Base(answerAbs))
	if rel, err := filepath.Rel(outputReal, answerReal); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", "", fmt.Errorf("--answer-file must be outside --output-dir")
	}
	if info, err := os.Lstat(answerReal); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", "", fmt.Errorf("--answer-file must be a new regular file path")
		}
		return "", "", fmt.Errorf("--answer-file already exists")
	} else if !os.IsNotExist(err) {
		return "", "", fmt.Errorf("inspect --answer-file: %w", err)
	}
	return filepath.Clean(outputReal), filepath.Clean(answerReal), nil
}

func ensureSpeechEvalRoot(outputRoot string) (string, error) {
	path := filepath.Join(outputRoot, "speech-eval")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := os.Mkdir(path, 0o755); err != nil {
			return "", fmt.Errorf("create speech-eval output directory: %w", err)
		}
		return path, nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect speech-eval output directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("speech-eval output path must be a regular directory")
	}
	return path, nil
}

func createSetDirectory(evalRoot string) (string, string, error) {
	for attempt := 0; attempt < 5; attempt++ {
		setID, err := randomOpaqueID()
		if err != nil {
			return "", "", err
		}
		path := filepath.Join(evalRoot, setID)
		if err := os.Mkdir(path, 0o755); err == nil {
			return setID, path, nil
		} else if !os.IsExist(err) {
			return "", "", fmt.Errorf("create speech-eval set directory: %w", err)
		}
	}
	return "", "", fmt.Errorf("create speech-eval set directory: random identifier collision")
}

func randomOpaqueID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", fmt.Errorf("generate opaque identifier: %w", err)
	}
	return hex.EncodeToString(data[:]), nil
}

func randomVariantOrder() (bool, error) {
	var bit [1]byte
	if _, err := rand.Read(bit[:]); err != nil {
		return false, fmt.Errorf("generate blinded variant order: %w", err)
	}
	return bit[0]&1 == 1, nil
}

func createOpaqueWAVPath(setDir string) (string, string, error) {
	for attempt := 0; attempt < 5; attempt++ {
		id, err := randomOpaqueID()
		if err != nil {
			return "", "", err
		}
		filename := id + ".wav"
		path := filepath.Join(setDir, filename)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return filename, path, nil
		} else if err != nil {
			return "", "", fmt.Errorf("inspect opaque WAV path: %w", err)
		}
	}
	return "", "", fmt.Errorf("generate opaque WAV path: random identifier collision")
}

func synthesizeVariant(ctx context.Context, bridge *tts.RenCrowTTSBridge, expectedProfileRevision int, token, sessionID, turnID string, plan speech.SpeechPlan, caseID, side string) (tts.SpeechPlanSynthesisResult, error) {
	result, err := bridge.SynthesizeSpeechPlanUtterance(ctx, tts.SpeechPlanSynthesisRequest{
		SessionID:      sessionID,
		TurnID:         turnID,
		SpeakerID:      "mio",
		UtteranceIndex: 0,
		Plan:           plan,
	})
	if err != nil {
		return tts.SpeechPlanSynthesisResult{}, fmt.Errorf("synthesize case %s %s: %w", caseID, side, redactSecret(err, token))
	}
	if result.ProfileRevision != expectedProfileRevision {
		return tts.SpeechPlanSynthesisResult{}, fmt.Errorf("case %s %s Gateway profile revision %d does not match approved revision %d", caseID, side, result.ProfileRevision, expectedProfileRevision)
	}
	if result.IrodoriSeed == nil {
		return tts.SpeechPlanSynthesisResult{}, fmt.Errorf("case %s %s Gateway response omitted the Irodori seed", caseID, side)
	}
	if result.SynthesisMS == nil {
		return tts.SpeechPlanSynthesisResult{}, fmt.Errorf("case %s %s Gateway response omitted synthesis timing", caseID, side)
	}
	if result.WAV == nil {
		return tts.SpeechPlanSynthesisResult{}, fmt.Errorf("case %s %s Gateway response omitted WAV audio", caseID, side)
	}
	return result, nil
}

func artifactFor(filename string, result tts.SpeechPlanSynthesisResult) speecheval.AudioArtifact {
	hash := sha256.Sum256(result.WAV)
	return speecheval.AudioArtifact{
		Filename:        filename,
		ProfileRevision: result.ProfileRevision,
		Seed:            *result.IrodoriSeed,
		SHA256:          hex.EncodeToString(hash[:]),
		SynthesisMS:     *result.SynthesisMS,
	}
}

func writeExclusiveFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
	}()
	if _, err := file.Write(data); err != nil {
		_ = os.Remove(path)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func writeAtomicManifest(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("existing manifest path is not a regular file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".speech-eval-manifest-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o644); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return nil
}

func redactSecret(err error, secret string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if secret != "" {
		message = strings.ReplaceAll(message, secret, "[REDACTED]")
	}
	return fmt.Errorf("%s", message)
}
