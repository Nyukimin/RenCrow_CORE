#!/usr/bin/env python3
"""Collect movie/person catalog records from eiga.com with link edges.

This tool is intentionally outside the RenCrow runtime. It stores raw catalog
facts in SQLite/JSONL so later import or validation can be done explicitly.
"""

from __future__ import annotations

import argparse
import gzip
import html
import hashlib
import json
import re
import sqlite3
import sys
import time
import urllib.parse
import urllib.request
import urllib.robotparser
import xml.etree.ElementTree as ET
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Iterable


BASE_URL = "https://eiga.com"
DEFAULT_USER_AGENT = "RenCrowLocalResearch/0.1 (+local user-run; robots-aware)"
ROBOTS_URL = f"{BASE_URL}/robots.txt"
SITEMAP_INDEX_URL = f"{BASE_URL}/sitemap/index.xml.gz"


@dataclass
class LinkedPerson:
    person_id: str
    name: str
    url: str
    role: str = ""


@dataclass
class LinkedMovie:
    movie_id: str
    title: str
    url: str
    role: str = ""


@dataclass
class MovieRecord:
    movie_id: str
    title: str
    url: str
    synopsis: str = ""
    cast: list[LinkedPerson] = field(default_factory=list)
    staff: list[LinkedPerson] = field(default_factory=list)
    related_people: list[LinkedPerson] = field(default_factory=list)


@dataclass
class PersonRecord:
    person_id: str
    name: str
    url: str
    profile: dict[str, str] = field(default_factory=dict)
    biography: str = ""
    biography_movies: list[LinkedMovie] = field(default_factory=list)
    filmography: list[LinkedMovie] = field(default_factory=list)


def normalize_url(url: str) -> str:
    url = html.unescape(url.strip())
    return urllib.parse.urljoin(BASE_URL, url)


def entity_id(url: str, kind: str) -> str:
    match = re.search(rf"/{kind}/(\d+)/", url)
    return match.group(1) if match else ""


def clean_text(value: str) -> str:
    value = re.sub(r"<br\s*/?>", "\n", value, flags=re.I)
    value = re.sub(r"<[^>]+>", "", value)
    value = html.unescape(value)
    value = re.sub(r"[ \t\r\f\v]+", " ", value)
    value = re.sub(r"\n\s+", "\n", value)
    return value.strip()


def normalize_title_key(value: str) -> str:
    value = clean_text(value)
    value = value.replace("（", "(").replace("）", ")")
    value = re.sub(r"^\((?:[^)]*(?:字|字幕|吹|吹替|MX4D|3D|AT|中継|舞台挨拶|先行上映)[^)]*)\)", "", value, flags=re.I)
    value = re.sub(r"^(?:映画|劇場版)\s*[『「]?", "", value)
    value = re.sub(r"[『』「」]", "", value)
    value = value.strip()
    try:
        import unicodedata

        value = unicodedata.normalize("NFKC", value)
    except Exception:
        pass
    value = value.lower()
    value = re.sub(r"[\s　・\-.。:：!！?？()\[\]【】/／#＃×☆★〜～ー－―]", "", value)
    return value


def find_first(pattern: str, text: str, flags: int = re.S | re.I) -> str:
    match = re.search(pattern, text, flags)
    return match.group(1) if match else ""


def extract_ldjson(html_text: str) -> list[object]:
    items: list[object] = []
    for raw in re.findall(r'<script\s+type=["\']application/ld\+json["\'][^>]*>(.*?)</script>', html_text, re.S | re.I):
        try:
            parsed = json.loads(html.unescape(raw.strip()))
        except json.JSONDecodeError:
            continue
        if isinstance(parsed, list):
            items.extend(parsed)
        else:
            items.append(parsed)
    return items


def person_from_schema(item: object, role: str = "") -> LinkedPerson | None:
    if not isinstance(item, dict):
        return None
    url = normalize_url(str(item.get("url") or ""))
    pid = entity_id(url, "person")
    name = clean_text(str(item.get("name") or ""))
    if not pid or not name:
        return None
    return LinkedPerson(person_id=pid, name=name, url=url, role=role)


def unique_people(items: Iterable[LinkedPerson]) -> list[LinkedPerson]:
    seen: set[tuple[str, str]] = set()
    out: list[LinkedPerson] = []
    for item in items:
        key = (item.person_id, item.role)
        if key in seen:
            continue
        seen.add(key)
        out.append(item)
    return out


def unique_movies(items: Iterable[LinkedMovie]) -> list[LinkedMovie]:
    seen: set[tuple[str, str]] = set()
    out: list[LinkedMovie] = []
    for item in items:
        key = (item.movie_id, item.role)
        if key in seen:
            continue
        seen.add(key)
        out.append(item)
    return out


def parse_movie(html_text: str, url: str) -> MovieRecord:
    url = normalize_url(url)
    movie_id = entity_id(url, "movie")
    title = clean_text(find_first(r'<h1[^>]*class=["\'][^"\']*page-title[^"\']*["\'][^>]*>(.*?)</h1>', html_text))
    synopsis = ""
    cast: list[LinkedPerson] = []
    staff: list[LinkedPerson] = []

    for item in extract_ldjson(html_text):
        if not isinstance(item, dict) or item.get("@type") != "Movie":
            continue
        title = title or clean_text(str(item.get("name") or ""))
        synopsis = clean_text(str(item.get("description") or ""))
        for actor in item.get("actor") or []:
            person = person_from_schema(actor, "出演")
            if person:
                cast.append(person)
        for director in item.get("director") or []:
            person = person_from_schema(director, "監督")
            if person:
                staff.append(person)

    if not synopsis:
        synopsis = clean_text(find_first(r'<h2[^>]*>\s*解説・あらすじ\s*</h2>\s*<p>(.*?)</p>', html_text))

    staff_block = find_first(r'<dl\s+class=["\']movie-staff["\'][^>]*>(.*?)</dl>', html_text)
    current_role = ""
    for tag, body in re.findall(r"<(dt|dd)[^>]*>(.*?)</\1>", staff_block, re.S | re.I):
        if tag.lower() == "dt":
            current_role = clean_text(body)
            continue
        href = find_first(r'href=["\'](/person/\d+/)["\']', body)
        name = clean_text(body)
        pid = entity_id(href, "person")
        if pid and name:
            staff.append(LinkedPerson(person_id=pid, name=name, url=normalize_url(href), role=current_role))

    cast_block = find_first(r'<ul\s+class=["\']movie-cast["\'][^>]*>(.*?)</ul>', html_text)
    for href, body in re.findall(r'<a[^>]+href=["\'](/person/\d+/)["\'][^>]*>(.*?)</a>', cast_block, re.S | re.I):
        pid = entity_id(href, "person")
        name = clean_text(body)
        if pid and name:
            cast.append(LinkedPerson(person_id=pid, name=name, url=normalize_url(href), role="出演"))

    related = unique_people([*cast, *staff])
    return MovieRecord(
        movie_id=movie_id,
        title=title,
        url=url,
        synopsis=synopsis,
        cast=unique_people(cast),
        staff=unique_people(staff),
        related_people=related,
    )


def parse_person(html_text: str, url: str, filmography_html: str = "") -> PersonRecord:
    url = normalize_url(url)
    person_id = entity_id(url, "person")
    name = clean_text(find_first(r'<h1[^>]*class=["\'][^"\']*page-title[^"\']*["\'][^>]*>(.*?)</h1>', html_text))
    profile: dict[str, str] = {}
    profile_block = find_first(r'<div\s+class=["\']profile["\'][^>]*>\s*<dl>(.*?)</dl>', html_text)
    current_key = ""
    for tag, attrs, body in re.findall(r"<(dt|dd)([^>]*)>(.*?)</\1>", profile_block, re.S | re.I):
        if tag.lower() == "dt":
            current_key = clean_text(body)
        elif current_key:
            if re.search(r'class=["\'][^"\']*\bsns\b', attrs, re.I):
                continue
            value = clean_text(body)
            if value:
                profile[current_key] = value
    bio_block = find_first(r'<div\s+class=["\']txt-block["\'][^>]*>\s*<p[^>]*class=["\']txt["\'][^>]*>(.*?)</p>', html_text)
    biography = clean_text(bio_block)
    biography_movies = movie_links_from_block(bio_block)
    filmography = movie_links_from_filmography(filmography_html) if filmography_html else []
    return PersonRecord(
        person_id=person_id,
        name=name,
        url=url,
        profile=profile,
        biography=biography,
        biography_movies=unique_movies(biography_movies),
        filmography=unique_movies(filmography),
    )


def movie_links_from_block(block: str) -> list[LinkedMovie]:
    out: list[LinkedMovie] = []
    for href, body in re.findall(r'<a[^>]+href=["\'](/movie/\d+/)["\'][^>]*>(.*?)</a>', block, re.S | re.I):
        mid = entity_id(href, "movie")
        title = clean_text(body)
        if mid and title:
            out.append(LinkedMovie(movie_id=mid, title=title, url=normalize_url(href)))
    return out


def movie_links_from_filmography(html_text: str) -> list[LinkedMovie]:
    out: list[LinkedMovie] = []
    for href, body in re.findall(r'<a[^>]+href=["\'](/movie/\d+/)["\'][^>]*>(.*?)</a>', html_text, re.S | re.I):
        mid = entity_id(href, "movie")
        title = clean_text(find_first(r'<p\s+class=["\']title["\'][^>]*>(.*?)</p>', body) or body)
        role = clean_text(find_first(r'<p\s+class=["\']label["\'][^>]*>(.*?)</p>', body))
        if mid and title:
            out.append(LinkedMovie(movie_id=mid, title=title, url=normalize_url(href), role=role))
    return out


class EigaStore:
    def __init__(self, db_path: Path, jsonl_path: Path):
        db_path.parent.mkdir(parents=True, exist_ok=True)
        jsonl_path.parent.mkdir(parents=True, exist_ok=True)
        self.conn = sqlite3.connect(db_path)
        self.jsonl_path = jsonl_path
        self._init_schema()

    def close(self) -> None:
        self.conn.close()

    def _init_schema(self) -> None:
        self.conn.executescript(
            """
            CREATE TABLE IF NOT EXISTS movies (
              movie_id TEXT PRIMARY KEY,
              title TEXT NOT NULL,
              url TEXT NOT NULL,
              synopsis TEXT,
              fetched_at TEXT DEFAULT CURRENT_TIMESTAMP
            );
            CREATE TABLE IF NOT EXISTS people (
              person_id TEXT PRIMARY KEY,
              name TEXT NOT NULL,
              url TEXT NOT NULL,
              profile_json TEXT,
              biography TEXT,
              fetched_at TEXT DEFAULT CURRENT_TIMESTAMP
            );
            CREATE TABLE IF NOT EXISTS movie_people (
              movie_id TEXT NOT NULL,
              person_id TEXT NOT NULL,
              role TEXT NOT NULL,
              source TEXT NOT NULL,
              movie_title TEXT,
              person_name TEXT,
              movie_url TEXT,
              person_url TEXT,
              PRIMARY KEY (movie_id, person_id, role, source)
            );
            CREATE TABLE IF NOT EXISTS fetch_log (
              url TEXT PRIMARY KEY,
              status TEXT NOT NULL,
              fetched_at TEXT DEFAULT CURRENT_TIMESTAMP,
              error TEXT
            );
            """
        )
        self.conn.commit()

    def append_jsonl(self, kind: str, payload: object) -> None:
        with self.jsonl_path.open("a", encoding="utf-8") as f:
            f.write(json.dumps({"kind": kind, **asdict(payload)}, ensure_ascii=False) + "\n")

    def save_movie(self, movie: MovieRecord) -> None:
        self.conn.execute(
            "INSERT OR REPLACE INTO movies(movie_id,title,url,synopsis) VALUES(?,?,?,?)",
            (movie.movie_id, movie.title, movie.url, movie.synopsis),
        )
        for person in movie.cast:
            self.save_edge(movie, person, person.role or "出演", "movie_cast")
        for person in movie.staff:
            self.save_edge(movie, person, person.role or "staff", "movie_staff")
        self.conn.commit()
        self.append_jsonl("movie", movie)

    def save_person(self, person: PersonRecord) -> None:
        self.conn.execute(
            "INSERT OR REPLACE INTO people(person_id,name,url,profile_json,biography) VALUES(?,?,?,?,?)",
            (person.person_id, person.name, person.url, json.dumps(person.profile, ensure_ascii=False), person.biography),
        )
        for movie in person.biography_movies:
            self.save_person_movie_edge(person, movie, movie.role or "略歴内関連作", "person_biography")
        for movie in person.filmography:
            self.save_person_movie_edge(person, movie, movie.role or "関連作品", "person_filmography")
        self.conn.commit()
        self.append_jsonl("person", person)

    def save_edge(self, movie: MovieRecord, person: LinkedPerson, role: str, source: str) -> None:
        self.conn.execute(
            """INSERT OR REPLACE INTO movie_people
               (movie_id,person_id,role,source,movie_title,person_name,movie_url,person_url)
               VALUES(?,?,?,?,?,?,?,?)""",
            (movie.movie_id, person.person_id, role, source, movie.title, person.name, movie.url, person.url),
        )

    def save_person_movie_edge(self, person: PersonRecord, movie: LinkedMovie, role: str, source: str) -> None:
        self.conn.execute(
            """INSERT OR REPLACE INTO movie_people
               (movie_id,person_id,role,source,movie_title,person_name,movie_url,person_url)
               VALUES(?,?,?,?,?,?,?,?)""",
            (movie.movie_id, person.person_id, role, source, movie.title, person.name, movie.url, person.url),
        )

    def mark_fetch(self, url: str, status: str, error: str = "") -> None:
        self.conn.execute(
            "INSERT OR REPLACE INTO fetch_log(url,status,error) VALUES(?,?,?)",
            (url, status, error),
        )
        self.conn.commit()


def init_watch_schema(conn: sqlite3.Connection) -> None:
    conn.executescript(
        """
        CREATE TABLE IF NOT EXISTS movie_watch_events (
          event_id TEXT PRIMARY KEY,
          movie_id TEXT,
          original_title TEXT NOT NULL,
          watched_at TEXT,
          source TEXT NOT NULL,
          source_batch_id TEXT,
          note TEXT,
          created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
        );
        CREATE TABLE IF NOT EXISTS movie_title_observations (
          observation_id TEXT PRIMARY KEY,
          original_title TEXT NOT NULL,
          normalized_title TEXT NOT NULL,
          source TEXT NOT NULL,
          source_batch_id TEXT,
          status TEXT NOT NULL DEFAULT 'unresolved',
          resolved_movie_id TEXT,
          resolution_note TEXT,
          created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
          resolved_at TEXT
        );
        CREATE INDEX IF NOT EXISTS idx_movie_watch_events_movie_id ON movie_watch_events(movie_id);
        CREATE INDEX IF NOT EXISTS idx_movie_watch_events_batch ON movie_watch_events(source_batch_id);
        CREATE INDEX IF NOT EXISTS idx_movie_title_observations_status ON movie_title_observations(status);
        """
    )
    conn.commit()


def init_preference_schema(conn: sqlite3.Connection) -> None:
    conn.executescript(
        """
        CREATE TABLE IF NOT EXISTS movie_preference_signals (
          signal_id TEXT PRIMARY KEY,
          signal_type TEXT NOT NULL,
          target_id TEXT,
          target_label TEXT NOT NULL,
          weight REAL NOT NULL DEFAULT 1.0,
          evidence_json TEXT NOT NULL,
          generated_by TEXT NOT NULL,
          generated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
        );
        CREATE INDEX IF NOT EXISTS idx_movie_preference_signals_target ON movie_preference_signals(target_id);
        CREATE INDEX IF NOT EXISTS idx_movie_preference_signals_type ON movie_preference_signals(signal_type);
        """
    )
    conn.commit()


def movie_title_candidates(conn: sqlite3.Connection) -> dict[str, list[tuple[str, str]]]:
    out: dict[str, list[tuple[str, str]]] = {}
    rows = conn.execute("SELECT movie_id, title FROM movies WHERE COALESCE(title, '') != ''").fetchall()
    rows.extend(
        conn.execute(
            """
            SELECT movie_id, movie_title
            FROM movie_people
            WHERE COALESCE(movie_id, '') != '' AND COALESCE(movie_title, '') != ''
            GROUP BY movie_id, movie_title
            """
        ).fetchall()
    )
    for movie_id, title in rows:
        key = normalize_title_key(str(title))
        if not key:
            continue
        out.setdefault(key, [])
        candidate = (str(movie_id), str(title))
        if candidate not in out[key]:
            out[key].append(candidate)
    return out


def normalize_person_key(value: str) -> str:
    value = clean_text(value)
    try:
        import unicodedata

        value = unicodedata.normalize("NFKC", value)
    except Exception:
        pass
    value = value.lower()
    value = re.sub(r"[\s　・\-.。:：!！?？()\[\]【】/／#＃]", "", value)
    return value


def person_name_candidates(conn: sqlite3.Connection) -> dict[str, list[tuple[str, str]]]:
    out: dict[str, list[tuple[str, str]]] = {}
    rows = conn.execute("SELECT person_id, name FROM people WHERE COALESCE(name, '') != ''").fetchall()
    rows.extend(
        conn.execute(
            """
            SELECT person_id, person_name
            FROM movie_people
            WHERE COALESCE(person_id, '') != '' AND COALESCE(person_name, '') != ''
            GROUP BY person_id, person_name
            """
        ).fetchall()
    )
    for person_id, name in rows:
        key = normalize_person_key(str(name))
        if not key:
            continue
        out.setdefault(key, [])
        candidate = (str(person_id), str(name))
        if candidate not in out[key]:
            out[key].append(candidate)
    return out


def resolve_person_name(candidates: dict[str, list[tuple[str, str]]], name: str) -> tuple[str, str, str]:
    key = normalize_person_key(name)
    if not key:
        return "", "unresolved", "empty normalized person name"
    exact = candidates.get(key, [])
    if len(exact) == 1:
        return exact[0][0], "resolved", "exact normalized person name match"
    if len(exact) > 1:
        return "", "candidate", "multiple exact normalized person name matches"

    partial: list[tuple[str, str]] = []
    if len(key) >= 3:
        for candidate_key, items in candidates.items():
            if key in candidate_key or candidate_key in key:
                partial.extend(items)
    unique = sorted(set(partial))
    if len(unique) == 1:
        return unique[0][0], "resolved", "single partial normalized person name match"
    if len(unique) > 1:
        return "", "candidate", "multiple partial normalized person name matches"
    return "", "unresolved", "no local person candidate"


def resolve_movie_title(candidates: dict[str, list[tuple[str, str]]], title: str) -> tuple[str, str, str]:
    key = normalize_title_key(title)
    if not key:
        return "", "unresolved", "empty normalized title"
    exact = candidates.get(key, [])
    if len(exact) == 1:
        return exact[0][0], "resolved", "exact normalized title match"
    if len(exact) > 1:
        return "", "candidate", "multiple exact normalized title matches"

    partial: list[tuple[str, str]] = []
    if len(key) >= 4:
        for candidate_key, items in candidates.items():
            if key in candidate_key or candidate_key in key:
                partial.extend(items)
    unique = sorted(set(partial))
    if len(unique) == 1:
        return unique[0][0], "resolved", "single partial normalized title match"
    if len(unique) > 1:
        return "", "candidate", "multiple partial normalized title matches"
    return "", "unresolved", "no local movie candidate"


def stable_id(prefix: str, *parts: object) -> str:
    raw = "\x1f".join(str(part) for part in parts)
    return prefix + "_" + hashlib.sha1(raw.encode("utf-8")).hexdigest()[:20]


def mark_watched_titles(
    db_path: Path,
    titles: list[str],
    watched_at: str,
    source: str,
    source_batch_id: str,
    note: str = "",
) -> dict[str, int]:
    conn = sqlite3.connect(db_path)
    try:
        init_watch_schema(conn)
        candidates = movie_title_candidates(conn)
        now = datetime.now(timezone.utc).isoformat()
        stats = {"input": 0, "resolved": 0, "candidate": 0, "unresolved": 0, "events": 0}
        for index, raw_title in enumerate(titles, start=1):
            title = clean_text(raw_title)
            if not title:
                continue
            stats["input"] += 1
            movie_id, status, resolution_note = resolve_movie_title(candidates, title)
            if status not in stats:
                stats[status] = 0
            stats[status] += 1
            event_id = stable_id("watch", source_batch_id, index, title)
            observation_id = stable_id("titleobs", source_batch_id, index, title)
            conn.execute(
                """
                INSERT OR REPLACE INTO movie_watch_events
                  (event_id,movie_id,original_title,watched_at,source,source_batch_id,note,created_at)
                VALUES(?,?,?,?,?,?,?,?)
                """,
                (event_id, movie_id or None, title, watched_at, source, source_batch_id, note, now),
            )
            stats["events"] += 1
            conn.execute(
                """
                INSERT OR REPLACE INTO movie_title_observations
                  (observation_id,original_title,normalized_title,source,source_batch_id,status,resolved_movie_id,resolution_note,created_at,resolved_at)
                VALUES(?,?,?,?,?,?,?,?,?,?)
                """,
                (
                    observation_id,
                    title,
                    normalize_title_key(title),
                    source,
                    source_batch_id,
                    status,
                    movie_id or None,
                    resolution_note,
                    now,
                    now if status == "resolved" else None,
                ),
            )
        conn.commit()
        return stats
    finally:
        conn.close()


def mark_favorite_people(
    db_path: Path,
    names: list[str],
    signal_type: str,
    source_batch_id: str,
    generated_by: str,
    weight: float = 1.0,
    note: str = "",
) -> dict[str, int]:
    conn = sqlite3.connect(db_path)
    try:
        init_preference_schema(conn)
        candidates = person_name_candidates(conn)
        now = datetime.now(timezone.utc).isoformat()
        stats = {"input": 0, "signals": 0, "resolved": 0, "candidate": 0, "unresolved": 0}
        for index, raw_name in enumerate(names, start=1):
            name = clean_text(raw_name)
            if not name:
                continue
            stats["input"] += 1
            person_id, status, resolution_note = resolve_person_name(candidates, name)
            if status not in stats:
                stats[status] = 0
            stats[status] += 1
            signal_id = stable_id("pref", signal_type, source_batch_id, index, name)
            evidence = {
                "source_batch_id": source_batch_id,
                "original_label": name,
                "status": status,
                "resolution_note": resolution_note,
                "note": note,
            }
            conn.execute(
                """
                INSERT OR REPLACE INTO movie_preference_signals
                  (signal_id,signal_type,target_id,target_label,weight,evidence_json,generated_by,generated_at)
                VALUES(?,?,?,?,?,?,?,?)
                """,
                (
                    signal_id,
                    signal_type,
                    person_id or None,
                    name,
                    weight,
                    json.dumps(evidence, ensure_ascii=False, sort_keys=True),
                    generated_by,
                    now,
                ),
            )
            stats["signals"] += 1
        conn.commit()
        return stats
    finally:
        conn.close()


class Fetcher:
    def __init__(self, user_agent: str, delay: float, timeout: float):
        self.user_agent = user_agent
        self.delay = delay
        self.timeout = timeout
        self.last_fetch = 0.0
        self.robots = urllib.robotparser.RobotFileParser()
        self.robots.set_url(ROBOTS_URL)
        self.robots.read()

    def allowed(self, url: str) -> bool:
        return self.robots.can_fetch(self.user_agent, url)

    def get(self, url: str) -> str:
        url = normalize_url(url)
        if not self.allowed(url):
            raise RuntimeError(f"robots.txt disallows fetch: {url}")
        wait = self.delay - (time.monotonic() - self.last_fetch)
        if wait > 0:
            time.sleep(wait)
        req = urllib.request.Request(url, headers={"User-Agent": self.user_agent})
        with urllib.request.urlopen(req, timeout=self.timeout) as res:
            data = res.read()
        self.last_fetch = time.monotonic()
        return data.decode("utf-8", "replace")

    def get_bytes(self, url: str) -> bytes:
        if not self.allowed(url):
            raise RuntimeError(f"robots.txt disallows fetch: {url}")
        req = urllib.request.Request(url, headers={"User-Agent": self.user_agent})
        with urllib.request.urlopen(req, timeout=self.timeout) as res:
            return res.read()


def sitemap_locations(fetcher: Fetcher, url: str = SITEMAP_INDEX_URL) -> list[str]:
    data = fetcher.get_bytes(url)
    text = gzip.decompress(data).decode("utf-8", "replace") if data[:2] == b"\x1f\x8b" else data.decode("utf-8", "replace")
    root = ET.fromstring(text)
    ns = {"sm": "http://www.sitemaps.org/schemas/sitemap/0.9"}
    return [el.text.strip() for el in root.findall(".//sm:loc", ns) if el.text]


def discover_catalog_urls(fetcher: Fetcher, max_sitemaps: int = 0) -> list[str]:
    out: list[str] = []
    sitemap_urls = sitemap_locations(fetcher)
    if max_sitemaps > 0:
        sitemap_urls = sitemap_urls[:max_sitemaps]
    for sitemap_url in sitemap_urls:
        for loc in sitemap_locations(fetcher, sitemap_url):
            if re.search(r"https://eiga\.com/(movie|person)/\d+/$", loc) and fetcher.allowed(loc):
                out.append(loc)
    return sorted(set(out))


def crawl(args: argparse.Namespace) -> int:
    fetcher = Fetcher(args.user_agent, args.delay, args.timeout)
    queue: list[str] = [normalize_url(url) for url in args.seed_url]
    if args.all_from_sitemap:
        queue.extend(discover_catalog_urls(fetcher, args.max_sitemaps))
    queue = sorted(set(queue))
    if args.dry_run:
        for url in queue[: args.max_pages or len(queue)]:
            print(url)
        print(f"dry_run_total={len(queue)}")
        return 0
    if args.max_pages <= 0:
        print("--max-pages is required unless --dry-run is used", file=sys.stderr)
        return 2

    store = EigaStore(args.db, args.jsonl)
    seen: set[str] = set()
    processed = 0
    try:
        while queue and processed < args.max_pages:
            url = queue.pop(0)
            if url in seen:
                continue
            seen.add(url)
            try:
                body = fetcher.get(url)
                if re.search(r"/movie/\d+/$", url):
                    movie = parse_movie(body, url)
                    store.save_movie(movie)
                    if args.follow_links:
                        queue.extend(p.url for p in movie.related_people if p.url not in seen)
                elif re.search(r"/person/\d+/$", url):
                    filmography_html = ""
                    if args.include_person_filmography:
                        filmography_url = url.rstrip("/") + "/movie/"
                        if fetcher.allowed(filmography_url):
                            filmography_html = fetcher.get(filmography_url)
                    person = parse_person(body, url, filmography_html)
                    store.save_person(person)
                    if args.follow_links:
                        queue.extend(m.url for m in [*person.biography_movies, *person.filmography] if m.url not in seen)
                else:
                    continue
                store.mark_fetch(url, "ok")
                processed += 1
                print(f"ok {processed}/{args.max_pages}: {url}", flush=True)
            except Exception as exc:  # noqa: BLE001 - CLI records and continues.
                store.mark_fetch(url, "error", str(exc))
                print(f"error: {url}: {exc}", file=sys.stderr, flush=True)
                processed += 1
    finally:
        store.close()
    return 0


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Collect eiga.com movie/person records and cross-links.")
    parser.add_argument("--mark-watched-file", type=Path, default=None, help="Plain text movie-title list to register as watched.")
    parser.add_argument("--watched-at", default="", help="Watched date/time for --mark-watched-file, e.g. 2026-06-03.")
    parser.add_argument("--watch-source", default="user_list", help="Source label for watched-title import.")
    parser.add_argument("--watch-batch-id", default="", help="Batch ID for watched-title import.")
    parser.add_argument("--watch-note", default="", help="Optional note for watched-title import.")
    parser.add_argument("--mark-favorite-people-file", type=Path, default=None, help="Plain text person-name list to register as favorite actors.")
    parser.add_argument("--favorite-signal-type", default="actor_affinity", help="Preference signal type for --mark-favorite-people-file.")
    parser.add_argument("--favorite-batch-id", default="", help="Batch ID for favorite-person import.")
    parser.add_argument("--favorite-generated-by", default="user", help="generated_by value for favorite-person import.")
    parser.add_argument("--favorite-weight", type=float, default=1.0, help="Preference weight for favorite-person import.")
    parser.add_argument("--favorite-note", default="", help="Optional note for favorite-person import.")
    parser.add_argument("--seed-url", action="append", default=[], help="Movie/person URL to start from. Repeatable.")
    parser.add_argument("--all-from-sitemap", action="store_true", help="Discover movie/person URLs from sitemap.")
    parser.add_argument("--max-sitemaps", type=int, default=0, help="Limit sitemap files for discovery; 0 means all.")
    parser.add_argument("--max-pages", type=int, default=0, help="Maximum pages to fetch. Required unless --dry-run.")
    parser.add_argument("--follow-links", action="store_true", help="Follow movie/person links discovered from parsed pages.")
    parser.add_argument("--include-person-filmography", action="store_true", help="Fetch /person/{id}/movie/ for full filmography links.")
    parser.add_argument("--output-dir", type=Path, default=Path("tmp/eiga_catalog"), help="Output directory.")
    parser.add_argument("--db", type=Path, default=None, help="SQLite output path.")
    parser.add_argument("--jsonl", type=Path, default=None, help="JSONL output path.")
    parser.add_argument("--delay", type=float, default=2.0, help="Minimum seconds between page fetches.")
    parser.add_argument("--timeout", type=float, default=20.0, help="HTTP timeout seconds.")
    parser.add_argument("--user-agent", default=DEFAULT_USER_AGENT, help="HTTP User-Agent.")
    parser.add_argument("--dry-run", action="store_true", help="Print planned URLs without fetching entity pages.")
    args = parser.parse_args(argv)
    args.output_dir.mkdir(parents=True, exist_ok=True)
    args.db = args.db or args.output_dir / "eiga_catalog.sqlite"
    args.jsonl = args.jsonl or args.output_dir / "eiga_catalog.jsonl"
    if args.mark_watched_file is not None:
        if not args.watched_at:
            args.watched_at = datetime.now(timezone.utc).date().isoformat()
        if not args.watch_batch_id:
            args.watch_batch_id = "watched_" + datetime.now(timezone.utc).strftime("%Y%m%d")
        return args
    if args.mark_favorite_people_file is not None:
        if not args.favorite_batch_id:
            args.favorite_batch_id = "favorite_people_" + datetime.now(timezone.utc).strftime("%Y%m%d")
        return args
    if not args.seed_url and not args.all_from_sitemap:
        parser.error("provide --seed-url or --all-from-sitemap")
    return args


def main(argv: list[str]) -> int:
    args = parse_args(argv)
    if args.mark_watched_file is not None:
        titles = sys.stdin.read().splitlines() if str(args.mark_watched_file) == "-" else args.mark_watched_file.read_text(encoding="utf-8").splitlines()
        stats = mark_watched_titles(args.db, titles, args.watched_at, args.watch_source, args.watch_batch_id, args.watch_note)
        print(json.dumps(stats, ensure_ascii=False, sort_keys=True))
        return 0
    if args.mark_favorite_people_file is not None:
        names = sys.stdin.read().splitlines() if str(args.mark_favorite_people_file) == "-" else args.mark_favorite_people_file.read_text(encoding="utf-8").splitlines()
        stats = mark_favorite_people(
            args.db,
            names,
            args.favorite_signal_type,
            args.favorite_batch_id,
            args.favorite_generated_by,
            args.favorite_weight,
            args.favorite_note,
        )
        print(json.dumps(stats, ensure_ascii=False, sort_keys=True))
        return 0
    return crawl(args)


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
