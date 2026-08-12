import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

function loadMovieDB() {
  const source = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/movie-db.js', 'utf8');
  const context = {
    console,
    URLSearchParams,
    document: {
      addEventListener() {},
      getElementById() { return null; },
      querySelectorAll() { return []; },
      querySelector() { return null; },
    },
    window: {
      setTimeout() {},
      clearTimeout() {},
      alert() {},
    },
    fetch() { return Promise.reject(new Error('fetch not available in unit test')); },
    esc(value) { return String(value ?? '').replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;'); },
    escAttr(value) { return String(value ?? '').replaceAll('&', '&amp;').replaceAll('"', '&quot;').replaceAll('<', '&lt;').replaceAll('>', '&gt;'); },
  };
  vm.createContext(context);
  vm.runInContext(source, context);
  return context;
}

test('person-related rendering escapes dynamic names and summaries while preserving both names', () => {
  const context = loadMovieDB();
  const html = context.movieDbRenderPersonRelatedItemsHTML({
    items: [{
      display_name: '<b>表示名</b>',
      name_original: 'Original "name" & more',
      summary_ja: '<img src=x onerror=alert(1)>',
      summary_state: 'translated_summary',
      relation_type: '出演',
      source: 'official_public',
    }],
    summary_coverage: {ready: 1, unavailable: 0, total: 1},
  });

  assert.match(html, /表示名/);
  assert.match(html, /Original/);
  assert.match(html, /translated_summary/);
  assert.doesNotMatch(html, /<b>表示名<\/b>/);
  assert.doesNotMatch(html, /<img src=x/);
  assert.match(html, /&lt;b&gt;表示名&lt;\/b&gt;/);
});

test('person-related category tabs render all categories and preserve selected category', () => {
  const context = loadMovieDB();
  const html = context.movieDbPersonRelatedCategoryTabsHTML('person-7', 'manga');

  for (const [category, label] of [
    ['movie', '映画'],
    ['drama', 'ドラマ'],
    ['award', '受賞歴'],
    ['music', '音楽'],
    ['anime', 'アニメ'],
    ['novel', '小説'],
    ['manga', '漫画'],
  ]) {
    assert.match(html, new RegExp(`data-person-related-category="${category}"`));
    assert.match(html, new RegExp(label));
  }
  assert.match(html, /data-person-related-category="manga"[^>]*aria-selected="true"/);
  assert.match(html, /data-person-id="person-7"/);
});

test('independent person-related panel preserves the row-only movie/person lists', () => {
  const js = fs.readFileSync('internal/adapter/viewer/assets/js/tabs/movie-db.js', 'utf8');
  const html = fs.readFileSync('internal/adapter/viewer/viewer.html', 'utf8');
  const css = fs.readFileSync('internal/adapter/viewer/assets/css/tabs/desk.css', 'utf8');

  assert.match(js, /\/viewer\/movie-catalog\/person-related\?' \+ params\.toString/);
  assert.match(js, /data-person-related-category/);
  assert.match(js, /movieDbLoadPersonRelated/);
  assert.match(html, /data-tab="person-related-catalog"[^>]*>人物関連情報/);
  assert.match(html, /id="panel-person-related-catalog"/);
  assert.match(html, /id="personRelatedPersonSelect"/);
  assert.match(html, /id="movieDbPersonRelated"/);
  assert.doesNotMatch(js, /\.movie-db-row:not\(\[data-detail-bound\]\)/);
  assert.match(js, /person-related\/people\?limit=1000/);
  assert.match(css, /movie-db-person-related/);
  assert.match(css, /@media[\s\S]*movie-db-person-related/);
});
