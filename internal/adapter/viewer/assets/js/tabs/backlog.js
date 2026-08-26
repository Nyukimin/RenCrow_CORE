'use strict';

// The former Backlog desk wrote an independent status board directly. Atlas is
// now the only CORE-owned lifecycle and this compatibility tab only navigates
// to that canonical GUI.
function openCanonicalAtlasBacklog() {
  const atlasTab = document.querySelector('[data-tab="atlas"]');
  if (atlasTab instanceof HTMLElement) atlasTab.click();
  if (typeof atlasActiveTab !== 'undefined') atlasActiveTab = 'backlog';
  if (typeof atlasRender === 'function') atlasRender();
  if (typeof refreshAtlas === 'function') refreshAtlas();
}

const backlogOpenAtlasBtn = document.getElementById('backlogOpenAtlasBtn');
if (backlogOpenAtlasBtn) backlogOpenAtlasBtn.addEventListener('click', openCanonicalAtlasBacklog);
