import assert from "node:assert/strict";
import {createHash} from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";
import { chromium, firefox } from "playwright";

const baseURL = process.env.RENCROW_E2E_BASE_URL;
const runtimeDir = process.env.RENCROW_E2E_RUNTIME;
const artifactDir = process.env.RENCROW_E2E_ARTIFACT_DIR;
const scenario = process.env.RENCROW_E2E_SCENARIO;
const browserName = process.env.RENCROW_E2E_BROWSER || "chromium";
const taskCenterKind = "movie.catalog.lookup";

function requireEnvironment(value, label) {
  assert.ok(value, label + " is required");
  return value;
}

async function readTestCredentials() {
  const tokenPath = path.join(runtimeDir, "local-agent-ops.token");
  const token = (await fs.readFile(tokenPath, "utf8")).trim();
  assert.ok(token.length >= 32, "test token must be present and sufficiently long");
  return token;
}

function authHeaders(token, additional = {}) {
  return Object.assign({
    Authorization: "Bearer " + token,
    "X-RenCrow-Client": "RenCrow_Viewer",
    "X-RenCrow-Interaction-Profile": "task-center",
    Accept: "application/json",
  }, additional);
}

async function apiRequest(pathname, options = {}) {
  const headers = Object.assign(
    {Accept: "application/json"},
    options.auth === false ? {} : authHeaders(options.token, options.headers),
  );
  if (options.body !== undefined) headers["Content-Type"] = "application/json";
  const response = await fetch(new URL(pathname, baseURL), {
    method: options.method || "GET",
    headers,
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
    redirect: "manual",
  });
  const raw = await response.text();
  let payload = null;
  try {
    payload = JSON.parse(raw);
  } catch (_) {
    payload = null;
  }
  return {status: response.status, raw, payload};
}

function assertSafeError(result, expectedStatus) {
  const errorCode = result.payload && typeof result.payload.error === "string" ? result.payload.error : "non_json_error";
  assert.equal(result.status, expectedStatus, "unexpected HTTP response status; safe_error=" + errorCode);
  assert.ok(result.payload && typeof result.payload === "object", "expected a JSON error response");
  assert.equal(typeof result.payload.error, "string", "expected a safe error code");
  assert.ok(!result.raw.includes(runtimeDir), "error response exposed the runtime path");
}

function assertDailyList(result) {
  assert.equal(result.status, 200, "authenticated Daily reads return a typed read-only envelope");
  const payload = result.payload;
  assert.ok(payload && Array.isArray(payload.definitions), "Daily response must use definitions, not Goal items");
  assert.ok(["available", "partial", "unavailable"].includes(payload.availability), "Daily availability state is missing");
  assert.equal(typeof payload.partial, "boolean");
  assert.equal(payload.partial, payload.availability !== "available");
  assert.ok(Array.isArray(payload.issues), "Daily envelope must preserve source issues");
  assert.ok(typeof payload.observed_at === "string" && !Number.isNaN(Date.parse(payload.observed_at)),
    "Daily envelope must include its observed_at timestamp");
  assert.ok(payload.definitions.length > 0, "registered Daily owners must retain definition rows");
  assert.ok(payload.definitions.every((definition) =>
    definition !== null && typeof definition === "object" &&
    typeof definition.definition_id === "string" && typeof definition.kind_id === "string" &&
    typeof definition.label === "string" && typeof definition.owner_ref === "string" && !("goal_id" in definition)),
  "Daily definitions must remain owner rows and must not be fabricated managed Goals");
  assert.ok(!result.raw.includes(runtimeDir), "Daily response exposed the isolated runtime path");
  return payload;
}

async function verifyUnavailableProfile(token) {
  const pageResponse = await fetch(new URL("/viewer/task-center", baseURL));
  assert.equal(pageResponse.status, 200, "TaskCenter page route should remain available");

  const unauthenticated = await apiRequest("/api/v1/task-center/capabilities", {auth: false});
  assertSafeError(unauthenticated, 401);

  const capabilitiesResponse = await apiRequest("/api/v1/task-center/capabilities", {token});
  assert.equal(capabilitiesResponse.status, 200, "registered daily owners keep capabilities readable without managed Workstream");
  const capabilities = capabilitiesResponse.payload.capabilities || capabilitiesResponse.payload;
  assert.equal(capabilities.managed_available, false);
  assert.equal(capabilities.daily_available, true);
  assert.equal(capabilities.can_submit, false);
  assert.equal(capabilities.can_claim, false);
  assert.equal(capabilities.managed_reason_code, "managed_owner_unavailable");
  assert.ok(Array.isArray(capabilities.kinds), "capabilities must preserve the managed kind field shape");

  const daily = await apiRequest("/api/v1/task-center/items?view=daily&limit=50", {token});
  const dailyPayload = assertDailyList(daily);

  const policyRevision = createHash("sha256").update(JSON.stringify({
    schema_version: 1,
    task_kinds: {
      [taskCenterKind]: {allowed_agents: {mode: "all_registered"}},
    },
  }), "utf8").digest("hex");
  const unavailableSubmit = await apiRequest("/api/v1/task-center/requests", {
    token,
    method: "POST",
    body: {
      data_scope: "public",
      kind_id: taskCenterKind,
      kind_input: {kind: "movie", name: "Task Center E2E Film", information: "overview", limit: 1},
      title: "TaskCenter managed owner unavailable fixture",
      description: "A complete typed request verifies that managed submission remains unavailable.",
      success_criteria: ["No Goal is created while the managed Workstream owner is unavailable."],
      verification: ["The authenticated request returns the managed unavailable code."],
      assignee: "unassigned",
      timing: taskTimingWithDeadline(),
      idempotency_key: "task-center-e2e-unavailable-" + Date.now(),
      expected_policy_revision: policyRevision,
    },
  });
  assertSafeError(unavailableSubmit, 503);
  assert.equal(unavailableSubmit.payload.error, "task_center_unavailable");
  console.log("PASS unavailable managed profile: page=200 unauthenticated=401 capabilities=200 daily=" + dailyPayload.availability + " submit=503");
}

function taskTimingWithDeadline() {
  return {
    timezone: "Asia/Tokyo",
    scheduled_start_at: null,
    not_before: null,
    allowed_windows: [],
    deadline_at: null,
    recurrence: null,
    estimated_duration_seconds: null,
  };
}

function typedInputForKind(kind, lookupName) {
  assert.ok(kind && kind.kind_id === taskCenterKind, "MovieCatalog kind descriptor is required");
  const schema = kind.input_schema;
  assert.ok(schema && schema.schema_id === kind.kind_id && schema.version === 1 && Array.isArray(schema.fields),
    "typed MovieCatalog input schema is missing or malformed");

  const values = Object.create(null);
  const controllers = new Map();
  const movieSelector = schema.fields.find((field) => field.type === "enum" &&
    Array.isArray(field.options) && field.options.some((option) => option && option.value === "movie"));

  for (const field of schema.fields) {
    let value;
    if (field.type === "enum") {
      const optionValues = Array.isArray(field.options) ? field.options.map((option) => option && option.value) : [];
      value = field === movieSelector ? "movie" :
        typeof field.default === "string" ? field.default : optionValues.find((candidate) => typeof candidate === "string");
      if (typeof value === "string") controllers.set(field.name, value);
    } else if (field.type === "enum_by_kind") {
      const categories = field.options_by_kind && typeof field.options_by_kind === "object"
        ? Object.keys(field.options_by_kind)
        : [];
      const controller = schema.fields.find((candidate) => candidate.type === "enum" &&
        categories.length > 0 && categories.every((category) => Array.isArray(candidate.options) &&
          candidate.options.some((option) => option && option.value === category)));
      const controllerValue = controller ? controllers.get(controller.name) || "" : "";
      const options = field.options_by_kind && Array.isArray(field.options_by_kind[controllerValue])
        ? field.options_by_kind[controllerValue]
        : [];
      value = typeof field.default === "string" && options.some((option) => option && option.value === field.default)
        ? field.default
        : options.find((option) => option && typeof option.value === "string")?.value;
    } else if (field.type === "string") {
      value = field.name === "name" ? lookupName :
        typeof field.default === "string" && field.default ? field.default :
          field.required ? "Task Center E2E input" : undefined;
    } else if (field.type === "integer") {
      value = Number.isSafeInteger(field.default) ? field.default : field.required ? field.minimum : undefined;
    } else {
      assert.fail("MovieCatalog schema contains an unsupported field type: " + String(field.type));
    }

    if (value !== undefined) values[field.name] = value;
    if (field.required) assert.ok(Object.prototype.hasOwnProperty.call(values, field.name), "required schema field has no fixture value: " + field.name);
  }
  assert.ok(Object.values(values).includes(lookupName), "MovieCatalog schema has no name field for the local fixture");
  return values;
}

async function fillTypedInputForm(page, kind, values) {
  for (const field of kind.input_schema.fields) {
    if (!Object.prototype.hasOwnProperty.call(values, field.name)) continue;
    const control = page.locator('[data-kind-input-name="' + field.name + '"]');
    await control.waitFor({state: "visible"});
    if (field.type === "enum" || field.type === "enum_by_kind") {
      await control.selectOption(String(values[field.name]));
    } else {
      await control.fill(String(values[field.name]));
    }
  }
}

function rawJSONProperty(source, wantedName) {
  function skipWhitespace(offset) {
    while (offset < source.length && /[\t\n\r ]/.test(source[offset])) offset++;
    return offset;
  }

  function stringEnd(start) {
    assert.equal(source[start], '"', "expected a JSON string");
    for (let offset = start + 1; offset < source.length; offset++) {
      if (source[offset] === "\\") {
        offset++;
        continue;
      }
      if (source[offset] === '"') return offset + 1;
    }
    throw new Error("unterminated JSON string");
  }

  function valueEnd(start) {
    const first = source[start];
    if (first === '"') return stringEnd(start);
    if (first === "{" || first === "[") {
      let depth = 0;
      let inString = false;
      let escaped = false;
      for (let offset = start; offset < source.length; offset++) {
        const character = source[offset];
        if (inString) {
          if (escaped) escaped = false;
          else if (character === "\\") escaped = true;
          else if (character === '"') inString = false;
          continue;
        }
        if (character === '"') inString = true;
        else if (character === "{" || character === "[") depth++;
        else if (character === "}" || character === "]") {
          depth--;
          if (depth === 0) return offset + 1;
        }
      }
      throw new Error("unterminated JSON container");
    }
    let offset = start;
    while (offset < source.length && !/[,}\]\t\n\r ]/.test(source[offset])) offset++;
    if (offset === start) throw new Error("empty JSON value");
    return offset;
  }

  let offset = skipWhitespace(0);
  assert.equal(source[offset], "{", "result response must be a JSON object");
  offset = skipWhitespace(offset + 1);
  while (offset < source.length && source[offset] !== "}") {
    const keyEnd = stringEnd(offset);
    const key = JSON.parse(source.slice(offset, keyEnd));
    offset = skipWhitespace(keyEnd);
    assert.equal(source[offset], ":", "expected a JSON object member separator");
    const start = skipWhitespace(offset + 1);
    const end = valueEnd(start);
    if (key === wantedName) return source.slice(start, end);
    offset = skipWhitespace(end);
    if (source[offset] === ",") offset = skipWhitespace(offset + 1);
    else if (source[offset] !== "}") throw new Error("expected a JSON object member delimiter");
  }
  throw new Error("JSON response did not contain " + wantedName);
}

async function verifyAPIContracts(token, capabilities, privateItem, foreignGoalID, kindInput) {
  const foreignDetail = await apiRequest("/api/v1/task-center/item?goal_id=" + encodeURIComponent(foreignGoalID), {token});
  assertSafeError(foreignDetail, 404);

  const listPath = "/api/v1/task-center/items?view=remaining&limit=50";
  const list = await apiRequest(listPath, {token});
  assert.equal(list.status, 200, "authenticated list should be readable");
  assert.ok(Array.isArray(list.payload.items), "list response should contain items");
  assert.ok(!list.raw.includes("FOREIGN_SCOPE_PRIVATE_SENTINEL"), "another user's private Goal leaked into list");
  assert.ok(!list.payload.items.some((item) => item.goal_id === foreignGoalID), "foreign private Goal ID leaked into list");

  const publicPayload = {
    data_scope: "public",
    kind_id: taskCenterKind,
    kind_input: kindInput,
    title: "TaskCenter E2E public " + browserName + " " + Date.now(),
    description: "Public task fixture created through the authenticated real API.",
    success_criteria: ["The public Goal is visible to the authenticated user."],
    verification: ["Read the Goal back through the real TaskCenter API."],
    assignee: "unassigned",
    timing: taskTimingWithDeadline(),
    idempotency_key: "task-center-e2e-public-" + browserName + "-" + Date.now(),
    expected_policy_revision: capabilities.policy_revision,
  };
  const publicCreate = await apiRequest("/api/v1/task-center/requests", {
    token,
    method: "POST",
    headers: {"X-RenCrow-Actor": "midori"},
    body: publicPayload,
  });
  assert.ok([200, 201].includes(publicCreate.status), "public Goal submission with an actor header should succeed as the authenticated user");
  assert.equal(publicCreate.payload.item.data_scope, "public");
  assert.equal(publicCreate.payload.item.assignee, "unassigned", "an untrusted actor header changed the assignee");
  assert.equal(publicCreate.payload.item.task_id || "", "", "public plan submission must not imply Task execution");
  assert.equal(publicCreate.payload.item.run_id || "", "", "public plan submission must not imply Run execution");
  assert.ok(!["completed", "started", "running"].includes(publicCreate.payload.item.status), "public plan submission was reported as executed");

  const actorSpoof = await apiRequest("/api/v1/task-center/requests", {
    token,
    method: "POST",
    body: Object.assign({}, publicPayload, {
      title: "TaskCenter actor spoof should be rejected",
      idempotency_key: "task-center-e2e-actor-spoof-" + browserName + "-" + Date.now(),
      actor_id: "midori",
    }),
  });
  assertSafeError(actorSpoof, 400);

  const claim = await apiRequest("/api/v1/task-center/claims", {
    token,
    method: "POST",
    body: {
      goal_id: privateItem.goal_id,
      expected_revision: privateItem.revision,
      expected_policy_revision: capabilities.policy_revision,
      idempotency_key: "task-center-e2e-http-claim-" + browserName + "-" + Date.now(),
    },
  });
  assertSafeError(claim, 403);

  const publicDetail = await apiRequest(
    "/api/v1/task-center/item?goal_id=" + encodeURIComponent(publicCreate.payload.item.goal_id),
    {token},
  );
  assert.equal(publicDetail.status, 200, "public Goal should be readable in the authenticated scope");
  assert.equal(publicDetail.payload.item.goal_id, publicCreate.payload.item.goal_id);

  const recurrence = Object.assign({}, publicPayload, {
    title: "TaskCenter unsupported recurrence should not be accepted",
    description: "A daily schedule must not be represented as a successful managed Goal.",
    idempotency_key: "task-center-e2e-recurrence-" + browserName + "-" + Date.now(),
    timing: Object.assign({}, taskTimingWithDeadline(), {
      scheduled_start_at: "2030-01-02T00:00:00Z",
      recurrence: {frequency: "daily", times: ["08:00"], weekdays: [], interval_seconds: null},
    }),
  });
  const recurrenceResult = await apiRequest("/api/v1/task-center/requests", {
    token,
    method: "POST",
    body: recurrence,
  });
  assert.equal(recurrenceResult.status, 503, "valid but unsupported daily recurrence should produce unavailable");
  assert.equal(recurrenceResult.payload.error, "task_center_unavailable", "recurrence rejection must reach the unsupported domain branch");
  assert.ok(!recurrenceResult.payload || !recurrenceResult.payload.item, "unsupported recurrence returned a successful Goal");
  console.log("PASS valid daily recurrence rejected: HTTP " + recurrenceResult.status + " error=" + recurrenceResult.payload.error);

  const daily = await apiRequest("/api/v1/task-center/items?view=daily&limit=50", {token});
  assertDailyList(daily);

  return publicCreate.payload.item;
}

async function connectPage(page, token) {
  await page.locator("#taskCenterToken").fill(token);
  await page.locator("#taskCenterConnectButton").click();
  await page.waitForFunction(() => {
    const state = document.querySelector("#taskCenterPageState");
    return state && state.textContent.includes("認証済み");
  });
  await page.locator("#taskCenterKind option[value='movie.catalog.lookup']").waitFor({state: "attached"});
}

async function ensureDetailsOpen(page, selector) {
  const details = page.locator(selector);
  await details.waitFor({state: "attached"});
  if (!(await details.evaluate((element) => element.open))) {
    await details.locator(":scope > summary").click();
  }
  assert.equal(await details.evaluate((element) => element.open), true, selector + " should be open");
}

async function submitPrivatePlan(page, browser, token, movieKind) {
  const privateTitle = "TaskCenter E2E private " + browser + " " + Date.now();
  const lookupName = "Task Center E2E Film";
  const expectedKindInput = typedInputForKind(movieKind, lookupName);
  const deadlineInput = "2030-01-02T03:04";

  await ensureDetailsOpen(page, "#taskCenterAddDetails");
  await ensureDetailsOpen(page, "#taskCenterAddForm details.task-center__timing");
  const dailyOption = page.locator("#taskCenterRecurrenceFrequency option[value='daily']");
  const dailyOptionState = await dailyOption.evaluate((option) => ({
    nodeName: option.nodeName,
    disabled: option.disabled,
    html: option.outerHTML,
    matchesDisabled: option.matches(":disabled"),
    selectDisabled: option.parentElement.disabled,
  }));
  dailyOptionState.count = await dailyOption.count();
  dailyOptionState.playwrightDisabled = await dailyOption.isDisabled();
  console.log("TaskCenter recurrence option: " + JSON.stringify(dailyOptionState));
  assert.equal(dailyOptionState.count, 1, "daily recurrence option selector should be unique");
  assert.equal(dailyOptionState.disabled, true, "unsupported daily recurrence should be natively disabled: " + JSON.stringify(dailyOptionState));
  assert.equal(dailyOptionState.matchesDisabled, true, "unsupported daily recurrence should match :disabled: " + JSON.stringify(dailyOptionState));
  assert.ok(
    (await page.locator(".task-center__recurrence").innerText()).includes("繰り返しタスクの追加は現在未対応"),
    "browser form should explain why recurrence is unavailable",
  );
  await page.locator("#taskCenterKind").selectOption(taskCenterKind);
  await fillTypedInputForm(page, movieKind, expectedKindInput);
  await page.locator("#taskCenterTitleInput").fill(privateTitle);
  await page.locator("#taskCenterDescription").fill("A private owner Goal created by the TaskCenter browser flow.");
  await page.locator("#taskCenterCriteria").fill("The Goal is retained by the Workstream SQLite owner.");
  await page.locator("#taskCenterVerification").fill("Reload the page and read the same Goal ID from the authenticated owner.");
  await page.locator("#taskCenterDeadline").fill(deadlineInput);

  const recurrenceSelect = page.locator("#taskCenterRecurrenceFrequency");
  await recurrenceSelect.focus();
  await recurrenceSelect.press("ArrowDown");
  await recurrenceSelect.press("End");
  assert.equal(await recurrenceSelect.inputValue(), "", "keyboard navigation must leave disabled frequencies unselected");

  let submitRequestCount = 0;
  let submittedPayload = null;
  page.on("request", (request) => {
    const requestURL = new URL(request.url());
    if (requestURL.pathname === "/api/v1/task-center/requests" && request.method() === "POST") {
      submitRequestCount++;
      try { submittedPayload = JSON.parse(request.postData() || "{}"); } catch (_) { submittedPayload = null; }
    }
  });
  await recurrenceSelect.evaluate((select) => {
    const daily = select.querySelector("option[value='daily']");
    daily.disabled = false;
    select.value = "daily";
    select.dispatchEvent(new Event("change", {bubbles: true}));
  });
  await page.locator("#taskCenterSubmit").click();
  await page.waitForFunction(() => {
    const error = document.querySelector("#taskCenterFormError");
    return error && !error.hidden && error.textContent.includes("繰り返しタスクの追加は現在未対応");
  });
  assert.equal(submitRequestCount, 0, "client-side recurrence validation must reject before POST");
  await recurrenceSelect.evaluate((select) => {
    const daily = select.querySelector("option[value='daily']");
    daily.disabled = true;
    select.value = "";
    select.dispatchEvent(new Event("change", {bubbles: true}));
  });

  const responsePromise = page.waitForResponse((response) => {
    const requestURL = new URL(response.url());
    return requestURL.pathname === "/api/v1/task-center/requests" && response.request().method() === "POST";
  });
  await page.locator("#taskCenterSubmit").click();
  const response = await responsePromise;
  assert.equal(submitRequestCount, 1, "only the valid plan should be submitted by the browser");
  assert.ok([200, 201].includes(response.status()), "browser form submission did not succeed");
  const mutation = await response.json();
  assert.ok(submittedPayload, "browser request body was not valid JSON");
  assert.deepEqual(
    submittedPayload.kind_input,
    JSON.parse(JSON.stringify(expectedKindInput)),
    "browser did not send the owner-described typed fields",
  );
  assert.ok(mutation.item && mutation.item.goal_id, "browser submission response did not contain the owner Goal");
  assert.equal(mutation.item.title, privateTitle);
  assert.equal(mutation.item.data_scope, "user", "operator-created form task should be private to the authenticated user");
  assert.equal(mutation.item.timing_status, "known");
  assert.equal(mutation.item.timing_source, "workstream.goal.task_plan");
  assert.equal(mutation.item.timing.timezone, "Asia/Tokyo");
  assert.ok(mutation.item.timing.deadline_at, "submitted deadline was not persisted");
  assert.equal(mutation.item.task_id || "", "", "adding a plan must not imply Task execution");
  assert.equal(mutation.item.run_id || "", "", "adding a plan must not imply Run execution");
  assert.ok(!["completed", "started", "running"].includes(mutation.item.status), "adding a plan was reported as executed");

  await page.locator(".task-center__item").filter({hasText: privateTitle}).waitFor({state: "visible"});
  const receiptMessage = await page.locator("#taskCenterMutationMessage").innerText();
  assert.ok(receiptMessage.includes("実行開始は別の状態です"), "receipt should distinguish acceptance from execution");
  return mutation.item;
}

async function verifyDetailAndPersistence(page, token, privateItem, publicItem) {
  const detailResult = await apiRequest(
    "/api/v1/task-center/item?goal_id=" + encodeURIComponent(privateItem.goal_id),
    {token},
  );
  assert.equal(detailResult.status, 200);
  assert.equal(detailResult.payload.item.goal_id, privateItem.goal_id);
  assert.equal(detailResult.payload.item.data_scope, "user");

  const privateCard = page.locator(".task-center__item").filter({hasText: privateItem.title});
  await privateCard.locator("summary").click();
  const detailsText = await privateCard.locator(".task-center__item-detail").innerText();
  assert.ok(detailsText.includes("共通時間項目"), "item detail view was not rendered");
  assert.ok(detailsText.includes("期限:"), "deadline is missing from item details");
  assert.ok(detailsText.includes("2030"), "deadline date is missing from item details");
  assert.ok(detailsText.includes("実行予定: 指定なし"), "known null schedule was not rendered as unspecified");
  assert.ok(detailsText.includes("開始可能時刻: 指定なし"), "known null not-before was not rendered as unspecified");
  assert.ok(detailsText.includes("繰り返し: 指定なし"), "known null recurrence was not rendered as unspecified");
  assert.ok(detailsText.includes("想定所要時間: 指定なし"), "known null duration was not rendered as unspecified");
  assert.ok(!detailsText.includes("Task参照"), "unstarted plan unexpectedly has a Task reference");
  assert.ok(!detailsText.includes("Run参照"), "unstarted plan unexpectedly has a Run reference");

  const publicCard = page.locator(".task-center__item").filter({hasText: publicItem.title});
  await publicCard.waitFor({state: "visible"});
  assert.ok(!(await page.locator(".task-center__item").filter({hasText: "FOREIGN_SCOPE_PRIVATE_SENTINEL"}).count()), "foreign private Goal appeared in the browser list");
}

async function readPendingNativeResult(token, goalID) {
  const itemResponse = await apiRequest("/api/v1/task-center/item?goal_id=" + encodeURIComponent(goalID), {token});
  assert.equal(itemResponse.status, 200, "seeded native result Goal should be visible to its creator");
  const item = itemResponse.payload.item;
  assert.ok(item && item.goal_id === goalID, "seeded native Goal detail did not match its fixture ID");
  assert.equal(item.data_scope, "public", "native result fixture should be public and readable by the authenticated creator");
  assert.ok(item.task_id && item.run_id, "seeded result must reference persisted native Task and Run IDs");
  assert.ok(Array.isArray(item.success_criteria) && item.success_criteria.length > 0, "native result Goal is missing its review criteria");
  assert.ok(Array.isArray(item.verification) && item.verification.length > 0, "native result Goal is missing its review instructions");
  assert.equal(item.execution_verification, "verification_pending", "seeded native result must await explicit creator review");
  assert.equal(item.can_accept_result, true, "only the configured Goal creator may accept the native result");
  assert.ok(typeof item.result_artifact_id === "string" && item.result_artifact_id, "native result artifact reference is missing");
  assert.match(item.result_sha256 || "", /^[a-f0-9]{64}$/i, "native result digest is missing or malformed");

  const resultResponse = await apiRequest("/api/v1/task-center/result?goal_id=" + encodeURIComponent(goalID), {token});
  assert.equal(resultResponse.status, 200, "canonical native result read should succeed for the visible Goal");
  const view = resultResponse.payload;
  assert.equal(view.goal_id, goalID);
  assert.equal(view.revision, item.revision, "ResultView revision did not correlate with the current Goal");
  assert.equal(view.assignment_epoch, item.assignment_epoch, "ResultView assignment epoch did not correlate with the current Goal");
  assert.equal(view.result_artifact_id, item.result_artifact_id, "ResultView artifact reference did not correlate with the Goal");
  assert.equal(view.result_sha256, item.result_sha256, "ResultView digest did not correlate with the Goal");
  assert.equal(view.execution_verification, "verification_pending");

  const resultJSON = rawJSONProperty(resultResponse.raw, "result");
  const result = JSON.parse(resultJSON);
  assert.deepEqual(result, view.result, "raw canonical result bytes do not represent the ResultView JSON value");
  const digest = createHash("sha256").update(Buffer.from(resultJSON, "utf8")).digest("hex");
  assert.equal(digest, view.result_sha256.toLowerCase(), "canonical result body does not match the owner-verified SHA-256");
  assert.equal(result.kind, "movie", "native result was not produced by the typed movie lookup executor");
  assert.equal(result.name, "Task Center E2E Film", "native result did not use the isolated indexed catalog fixture");
  return {item, view, result};
}

async function waitForAcceptanceRecoveryUI(page, browser) {
  try {
    await page.waitForFunction(() => {
      const message = document.querySelector("#taskCenterMutationMessage");
      const recovery = document.querySelector("#taskCenterRecovery");
      const connect = document.querySelector("#taskCenterConnectButton");
      const token = document.querySelector("#taskCenterToken");
      return message && message.textContent.includes("作成者が確認し、Goalを完了") &&
        recovery && recovery.hidden && connect && !connect.disabled && token && !token.disabled;
    });
  } catch (error) {
    const diagnostic = await page.evaluate(() => {
      const text = (selector) => document.querySelector(selector)?.textContent || "";
      const node = (selector) => document.querySelector(selector);
      const recovery = node("#taskCenterRecovery");
      const errorPanel = node("#taskCenterError");
      const resultPanel = document.querySelector(".task-center__result-review");
      return {
        phase: "acceptance-recovery-ui",
        completionMessageVisible: text("#taskCenterMutationMessage").includes("作成者が確認し、Goalを完了"),
        mutationUnknownMessage: text("#taskCenterMutationMessage").includes("未確認"),
        recoveryVisible: Boolean(recovery && !recovery.hidden),
        errorVisible: Boolean(errorPanel && !errorPanel.hidden),
        errorMentionsReceiptMismatch: text("#taskCenterError").includes("受付照会のGoal・revision・成果物参照"),
        submitDisabled: Boolean(node("#taskCenterSubmit")?.disabled),
        tokenDisabled: Boolean(node("#taskCenterToken")?.disabled),
        connectDisabled: Boolean(node("#taskCenterConnectButton")?.disabled),
        kindSelected: Boolean(node("#taskCenterKind")?.value),
        resultPanelVisible: Boolean(resultPanel && resultPanel.getClientRects().length),
        acceptedResultLabelVisible: text(".task-center__result-review").includes("作成者が成果物を確認済み"),
      };
    });
    let screenshot = "not-captured";
    if (artifactDir) {
      const screenshotPath = path.join(artifactDir, "task-center-acceptance-recovery-timeout-" + browser + ".png");
      try {
        await page.screenshot({path: screenshotPath, fullPage: true});
        screenshot = path.basename(screenshotPath);
      } catch (_) {
        screenshot = "capture-failed";
      }
    }
    throw new Error("phase=acceptance-recovery-ui " + JSON.stringify(diagnostic) + " screenshot=" + screenshot + " cause=" + error.message);
  }
}

async function verifyNativeResultReviewAndAcceptance(page, token, goalID, browser, movieKind) {
  const {item, view, result} = await readPendingNativeResult(token, goalID);
  const card = page.locator(".task-center__item").filter({hasText: item.title});
  await card.waitFor({state: "visible"});
  await card.locator("summary").click();
  const detail = card.locator(".task-center__item-detail");
  await detail.getByRole("button", {name: "owner詳細を再取得"}).click();
  await detail.getByRole("button", {name: "保存された成果物を確認"}).waitFor({state: "visible"});
  const detailText = await detail.innerText();
  for (const criterion of item.success_criteria) assert.ok(detailText.includes(criterion), "native completion criterion was not visible in the Goal detail");
  for (const verification of item.verification) assert.ok(detailText.includes(verification), "native verification instruction was not visible in the Goal detail");
  assert.ok(detailText.includes(item.task_id), "native Task ID was not shown in the Goal detail");
  assert.ok(detailText.includes(item.run_id), "native Run ID was not shown in the Goal detail");

  await detail.getByRole("button", {name: "保存された成果物を確認"}).click();
  const resultPanel = detail.locator(".task-center__result-review");
  await resultPanel.getByText("成果物JSONを表示").waitFor({state: "visible"});
  assert.ok((await resultPanel.innerText()).includes("実行成功・Goal確認待ち"), "saved execution result was not distinguished from Goal completion");
  assert.ok((await resultPanel.innerText()).includes(view.result_artifact_id), "saved result artifact reference was not shown");
  assert.ok((await resultPanel.innerText()).includes(view.result_sha256), "saved result digest was not shown");
  assert.deepEqual(JSON.parse(await resultPanel.locator("pre").textContent()), result, "Viewer rendered a different canonical result body");
  const acceptButton = resultPanel.getByRole("button", {name: "成果物を確認してGoalを完了にする"});
  assert.equal(await acceptButton.isVisible(), true, "authorized creator could not review and accept the native result");

  if (artifactDir) {
    await fs.mkdir(artifactDir, {recursive: true});
    const addDetails = page.locator("#taskCenterAddDetails");
    if (!(await addDetails.evaluate((element) => element.open))) await addDetails.locator(":scope > summary").click();
    await page.locator("#taskCenterKind").selectOption(taskCenterKind);
    const typedControls = page.locator("#taskCenterKindInputFields [data-kind-input-name]");
    await typedControls.first().waitFor({state: "visible"});
    assert.equal(await typedControls.count(), movieKind.input_schema.fields.length,
      "actual capability schema fields were not all rendered beside the saved result");
    await page.setViewportSize({width: 1440, height: 1000});
    await page.screenshot({path: path.join(artifactDir, "task-center-result-pending-desktop-" + browser + ".png"), fullPage: true});
    await page.setViewportSize({width: 390, height: 844});
    const dimensions = await page.evaluate(() => ({viewport: document.documentElement.clientWidth, content: document.documentElement.scrollWidth}));
    assert.ok(dimensions.content <= dimensions.viewport + 2, "narrow result-review viewport has horizontal overflow: " + JSON.stringify(dimensions));
    await page.screenshot({path: path.join(artifactDir, "task-center-result-pending-narrow-" + browser + ".png"), fullPage: true});
    await page.setViewportSize({width: 1440, height: 1000});
  }

  let acceptancePosts = 0;
  let acceptanceBody = null;
  let acceptanceRecoveryURL = null;
  page.on("request", (request) => {
    const requestURL = new URL(request.url());
    if (requestURL.pathname === "/api/v1/task-center/result/accept" && request.method() === "POST") {
      acceptancePosts++;
      try { acceptanceBody = JSON.parse(request.postData() || "{}"); } catch (_) { acceptanceBody = null; }
    } else if (requestURL.pathname === "/api/v1/task-center/result/acceptance" && request.method() === "GET") {
      acceptanceRecoveryURL = requestURL;
    }
  });
  let mutation;
  if (browser === "firefox") {
    let committedResponse = null;
    await page.route("**/api/v1/task-center/result/accept", async (route) => {
      const upstream = await route.fetch();
      assert.equal(upstream.status(), 201, "new creator acceptance should commit through the real HTTP API");
      committedResponse = await upstream.json();
      await route.abort("failed");
    });
    await acceptButton.click();
    await page.locator("#taskCenterRecovery").waitFor({state: "visible"});
    assert.ok((await page.locator("#taskCenterMutationMessage").innerText()).includes("応答が届かず"),
      "committed acceptance with a dropped response should remain outcome-unknown");
    assert.equal(await page.locator("#taskCenterSubmit").isDisabled(), true, "unknown acceptance must keep new mutations locked");
    assert.ok(committedResponse && committedResponse.item && committedResponse.item.status === "completed",
      "the upstream acceptance did not commit before its response was dropped");
    const recoveryResponsePromise = page.waitForResponse((response) => {
      const requestURL = new URL(response.url());
      return requestURL.pathname === "/api/v1/task-center/result/acceptance" && response.request().method() === "GET";
    });
    await page.locator("#taskCenterRecoverButton").click();
    const recoveryResponse = await recoveryResponsePromise;
    assert.equal(recoveryResponse.status(), 200, "same-key acceptance receipt read should recover the committed mutation");
    mutation = await recoveryResponse.json();
    assert.equal(acceptanceRecoveryURL?.searchParams.get("goal_id"), goalID);
    assert.equal(acceptanceRecoveryURL?.searchParams.get("idempotency_key"), acceptanceBody?.idempotency_key);
    assert.equal(acceptanceRecoveryURL?.searchParams.has("token"), false, "credentials must not appear in the recovery URL");
    console.log("TaskCenter Firefox acceptance receipt metadata: " + JSON.stringify({
      status: mutation.item?.status || "",
      revision: mutation.item?.revision ?? null,
      assignment_epoch: mutation.item?.assignment_epoch ?? null,
      execution_verification: mutation.item?.execution_verification || "",
      artifact_present: Boolean(mutation.item?.result_artifact_id),
      digest_matches_upstream: mutation.item?.result_sha256 === committedResponse?.item?.result_sha256,
      receipt_ref_present: Boolean(mutation.receipt_ref),
      replayed: mutation.replayed === true,
    }));
    await waitForAcceptanceRecoveryUI(page, browser);
    assert.equal(await page.locator("#taskCenterRecovery").isVisible(), false, "confirmed receipt should hide the recovery action");
    assert.equal(await page.locator("#taskCenterConnectButton").isDisabled(), false, "confirmed receipt should release the mutation lock");
    assert.equal(await page.locator("#taskCenterToken").isDisabled(), false, "confirmed receipt should release the token lock");
    await page.locator("#taskCenterKind").selectOption(taskCenterKind);
    await page.waitForFunction(() => {
      const submit = document.querySelector("#taskCenterSubmit");
      return submit && !submit.disabled;
    });
    assert.equal(await page.locator("#taskCenterSubmit").isDisabled(), false,
      "a valid selected kind should be submittable after the pending mutation lock is released");
    assert.equal(mutation.item.result_sha256, committedResponse.item.result_sha256,
      "receipt recovery must return the actual committed result receipt");
  } else {
    const acceptanceResponsePromise = page.waitForResponse((response) => {
      const requestURL = new URL(response.url());
      return requestURL.pathname === "/api/v1/task-center/result/accept" && response.request().method() === "POST";
    });
    await acceptButton.click();
    const acceptanceResponse = await acceptanceResponsePromise;
    assert.equal(acceptanceResponse.status(), 201, "new creator acceptance should be recorded by the real HTTP API");
    mutation = await acceptanceResponse.json();
  }
  assert.equal(acceptancePosts, 1, "a successful result acceptance must use exactly one POST");
  assert.ok(acceptanceBody, "creator acceptance request body was not valid JSON");
  assert.deepEqual(Object.keys(acceptanceBody).sort(), [
    "expected_assignment_epoch", "expected_revision", "goal_id", "idempotency_key",
  ].sort());
  assert.equal(acceptanceBody.goal_id, goalID);
  assert.equal(acceptanceBody.expected_revision, item.revision);
  assert.equal(acceptanceBody.expected_assignment_epoch, item.assignment_epoch);
  assert.match(acceptanceBody.idempotency_key || "", /^[0-9a-f-]{36}$/i);
  assert.equal("actor_id" in acceptanceBody, false, "client must not choose a creator identity");
  assert.equal("user_id" in acceptanceBody, false, "client must not choose a creator identity");
  assert.equal(mutation.item.goal_id, goalID);
  assert.equal(mutation.item.status, "completed");
  assert.equal(mutation.item.execution_verification, "accepted");
  assert.equal(mutation.item.result_artifact_id, item.result_artifact_id);
  assert.equal(mutation.item.result_sha256, item.result_sha256);
  assert.equal(mutation.item.revision, item.revision + 1);
  assert.equal(mutation.item.assignment_epoch, item.assignment_epoch);
  assert.equal(mutation.item.task_id, item.task_id);
  assert.equal(mutation.item.run_id, item.run_id);
  assert.ok(mutation.receipt_ref, "accepted native result did not return its receipt reference");
  await page.waitForFunction(() => {
    const message = document.querySelector("#taskCenterMutationMessage");
    return message && message.textContent.includes("作成者が確認し、Goalを完了");
  });

  const acceptedItemResponse = await apiRequest("/api/v1/task-center/item?goal_id=" + encodeURIComponent(goalID), {token});
  assert.equal(acceptedItemResponse.status, 200);
  assert.equal(acceptedItemResponse.payload.item.status, "completed", "accepted Goal state was not persisted");
  assert.equal(acceptedItemResponse.payload.item.execution_verification, "accepted");
  assert.notEqual(acceptedItemResponse.payload.item.can_accept_result, true, "accepted result must no longer be accepted twice");
  const acceptedViewResponse = await apiRequest("/api/v1/task-center/result?goal_id=" + encodeURIComponent(goalID), {token});
  assert.equal(acceptedViewResponse.status, 200);
  assert.equal(acceptedViewResponse.payload.execution_verification, "accepted");
  assert.equal(acceptedViewResponse.payload.result_sha256, view.result_sha256);

  await page.reload({waitUntil: "domcontentloaded"});
  await connectPage(page, token);
  await page.locator('[data-task-view="history"]').click();
  const acceptedCard = page.locator(".task-center__item").filter({hasText: item.title});
  await acceptedCard.waitFor({state: "visible"});
  await acceptedCard.locator("summary").click();
  const acceptedDetail = acceptedCard.locator(".task-center__item-detail");
  await acceptedDetail.getByRole("button", {name: "owner詳細を再取得"}).click();
  await acceptedDetail.getByRole("button", {name: "保存された成果物を確認"}).waitFor({state: "visible"});
  await acceptedDetail.getByRole("button", {name: "保存された成果物を確認"}).click();
  const acceptedPanel = acceptedDetail.locator(".task-center__result-review");
  await acceptedPanel.getByText("成果物JSONを表示").waitFor({state: "visible"});
  assert.ok((await acceptedPanel.innerText()).includes("作成者が成果物を確認済み"), "accepted result state did not survive page reload");
  assert.equal(await acceptedPanel.getByRole("button", {name: "成果物を確認してGoalを完了にする"}).count(), 0,
    "accepted result was offered for creator acceptance a second time");
  assert.equal(acceptancePosts, 1, "UI unexpectedly retried the result acceptance POST");
  console.log("PASS " + browser + " native Task/Run result read, digest, creator acceptance, and reload persistence");
}

async function captureResponsiveScreenshots(page, browser) {
  await fs.mkdir(artifactDir, {recursive: true});
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.setViewportSize({width: 1440, height: 1000});
  await page.screenshot({path: path.join(artifactDir, "task-center-desktop-" + browser + ".png"), fullPage: true});

  await page.setViewportSize({width: 390, height: 844});
  await page.waitForTimeout(200);
  const dimensions = await page.evaluate(() => ({
    viewport: document.documentElement.clientWidth,
    content: document.documentElement.scrollWidth,
  }));
  assert.ok(dimensions.content <= dimensions.viewport + 2, "narrow viewport has horizontal overflow: " + JSON.stringify(dimensions));
  await page.screenshot({path: path.join(artifactDir, "task-center-narrow-" + browser + ".png"), fullPage: true});
}

async function verifyConfiguredBrowser(token, foreignGoalID, pendingResultGoalID) {
  const engine = browserName === "firefox" ? firefox : chromium;
  const browser = await engine.launch({headless: true});
  try {
    const context = await browser.newContext({viewport: {width: 1440, height: 1000}});
    const page = await context.newPage();
    await page.goto(new URL("/viewer/task-center", baseURL).toString(), {waitUntil: "domcontentloaded"});
    assert.ok((await page.title()).toLowerCase().includes("task") || (await page.locator("h1").innerText()).includes("タスク"), "TaskCenter page did not load");

    await connectPage(page, token);
    const capabilitiesResult = await apiRequest("/api/v1/task-center/capabilities", {token});
    assert.equal(capabilitiesResult.status, 200, "capabilities should be available");
    const capabilities = capabilitiesResult.payload.capabilities || capabilitiesResult.payload;
    assert.ok(Array.isArray(capabilities.kinds) && capabilities.kinds.length > 0, "at least one registered kind should be reported");
    const movieKind = capabilities.kinds.find((kind) => kind.kind_id === taskCenterKind);
    assert.ok(movieKind, "MovieCatalog lookup kind was not registered");
    const renderedKindIDs = await page.locator("#taskCenterKind option").evaluateAll((options) =>
      options.map((option) => option.value).filter(Boolean).sort());
    assert.deepEqual(renderedKindIDs, capabilities.kinds.map((kind) => kind.kind_id).sort(),
      "the Viewer must render every registered capability, including planning-only kinds");
    const planningOnlyKind = capabilities.kinds.find((kind) => kind.executor_available === false && kind.kind_id !== taskCenterKind);
    if (planningOnlyKind) {
      await ensureDetailsOpen(page, "#taskCenterAddDetails");
      await page.locator("#taskCenterKind").selectOption(planningOnlyKind.kind_id);
      assert.ok((await page.locator("#taskCenterKindSupport").innerText()).includes("実行先は未接続"),
        "an unavailable executor must be described as planning-only");
      await page.locator("#taskCenterKind").selectOption(taskCenterKind);
    }
    assert.equal(capabilities.can_claim, false, "HTTP user must not be allowed to claim tasks");

    const lookupInput = typedInputForKind(movieKind, "Task Center E2E Film");
    const privateItem = await submitPrivatePlan(page, browserName, token, movieKind);
    const publicItem = await verifyAPIContracts(token, capabilities, privateItem, foreignGoalID, lookupInput);
    assert.equal(privateItem.status, "waiting", "private fixture should retain the native Goal waiting state");
    assert.equal(publicItem.status, "waiting", "public fixture should retain the native Goal waiting state");

    await page.reload({waitUntil: "domcontentloaded"});
    await connectPage(page, token);
    await page.locator(".task-center__item").filter({hasText: privateItem.title}).waitFor({state: "visible"});
    await page.locator(".task-center__item").filter({hasText: publicItem.title}).waitFor({state: "visible"});
    await verifyDetailAndPersistence(page, token, privateItem, publicItem);

    const dailyPayload = assertDailyList(await apiRequest("/api/v1/task-center/items?view=daily&limit=50", {token}));
    await page.locator('[data-task-view="daily"]').click();
    await page.waitForFunction(() => {
      const state = document.querySelector("#taskCenterListState");
      return state && (state.textContent.includes("日常定義:") || state.textContent.includes("日常定義を部分取得しました。") ||
        state.textContent.includes("日常定義sourceは観測不能です。"));
    });
    assert.equal(await page.locator("#taskCenterEmpty").isVisible(), false, "registered Daily owner rows were hidden as an empty list");
    const dailyRows = page.locator(".task-center__daily-definition");
    await dailyRows.first().waitFor({state: "visible"});
    const displayedDailyLabels = await dailyRows.locator(".task-center__item-title").allTextContents();
    const safeDailyLabels = dailyPayload.definitions.map((definition) => definition.label).filter((label) =>
      label && label.length <= 256 && !label.includes("@") && !label.includes("/") && !label.includes("\\") &&
      !/[\u0000-\u001f]/.test(label) && !/\b(?:bearer|password|passwd|secret|token|credential|api[_ -]?key)\b/i.test(label));
    assert.ok(safeDailyLabels.some((label) => displayedDailyLabels.includes(label)),
      "Daily Viewer did not render a human label from the authenticated owner rows");
    await page.locator('[data-task-view="remaining"]').click();
    await page.locator(".task-center__item").filter({hasText: privateItem.title}).waitFor({state: "visible"});
    await page.waitForFunction(() => {
      const pageState = document.querySelector("#taskCenterPageState");
      const mioState = document.querySelector("#taskCenterMioStatus");
      const summaryState = document.querySelector("#taskCenterSummaryState");
      const listState = document.querySelector("#taskCenterListState");
      return pageState && pageState.textContent.includes("認証済み・読み取り接続中") &&
        mioState && mioState.textContent === "読み取り接続中" &&
        summaryState && summaryState.textContent === "取得成功" &&
        listState && !listState.classList.contains("is-error") && !listState.classList.contains("is-recovered") &&
        (listState.textContent.includes("残タスク:") || listState.textContent.includes("部分取得です。現在の"));
    });
    assert.equal(await page.locator("#taskCenterError").isVisible(), false, "successful remaining-view read left a page-level error visible");
    const waitingStateLabels = await page.locator(".task-center__item-status").allTextContents();
    assert.ok(waitingStateLabels.length >= 2, "native Goal status labels were not rendered");
    assert.ok(waitingStateLabels.every((label) => !label.includes("未確認")), "known native waiting states were rendered as unknown: " + waitingStateLabels.join(" | "));
    assert.ok(waitingStateLabels.every((label) => label.includes("待機") || label.includes("受付")), "native waiting state has no waiting/acceptance label: " + waitingStateLabels.join(" | "));
    console.log("TaskCenter native waiting status labels: " + JSON.stringify(waitingStateLabels));

    await verifyNativeResultReviewAndAcceptance(page, token, pendingResultGoalID, browserName, movieKind);
    await captureResponsiveScreenshots(page, browserName);
    await context.close();
    console.log("PASS " + browserName + " TaskCenter: private/public persistence, cross-scope hiding, timing, non-execution, and responsive layout");
  } finally {
    await browser.close();
  }
}

async function main() {
  requireEnvironment(baseURL, "RENCROW_E2E_BASE_URL");
  requireEnvironment(runtimeDir, "RENCROW_E2E_RUNTIME");
  requireEnvironment(scenario, "RENCROW_E2E_SCENARIO");
  const token = await readTestCredentials();
  if (scenario === "unavailable") {
    await verifyUnavailableProfile(token);
    return;
  }
  assert.equal(scenario, "populated", "unknown E2E scenario");
  const foreignGoalID = (await fs.readFile(path.join(runtimeDir, "foreign-goal-id.txt"), "utf8")).trim();
  assert.ok(foreignGoalID, "private cross-scope fixture is missing");
  const pendingResultGoalID = (await fs.readFile(path.join(runtimeDir, "pending-result-goal-id-" + browserName + ".txt"), "utf8")).trim();
  assert.ok(pendingResultGoalID, "browser-specific native result fixture is missing for " + browserName);
  await verifyConfiguredBrowser(token, foreignGoalID, pendingResultGoalID);
}

main().catch((error) => {
  console.error("FAIL TaskCenter live browser E2E:", error && error.message ? error.message : String(error));
  process.exitCode = 1;
});
