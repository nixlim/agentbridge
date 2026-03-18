const state = {
  agents: {},
  tasks: [],
  goals: [],
  currentGoal: null,
  plan: null,
  workflow: {},
  workspace: {},
  workspaceFiles: [],
  messages: [],
  search: "",
  agentFilter: "",
  messageTypeFilter: new Set(),
  activeTaskInteractionId: "",
  pendingTaskRender: false,
  goalHistoryOpen: false,
};

const refs = {
  connectionDot: document.getElementById("connection-dot"),
  connectionLabel: document.getElementById("connection-label"),
  brainProviderLabel: document.getElementById("brain-provider-label"),
  workspacePath: document.getElementById("workspace-path"),
  refreshWorkspace: document.getElementById("refresh-workspace"),
  uploadWorkspace: document.getElementById("upload-workspace"),
  workspaceUploadInput: document.getElementById("workspace-upload-input"),
  workspaceUploadDir: document.getElementById("workspace-upload-dir"),
  workspaceTree: document.getElementById("workspace-tree"),
  startGoal: document.getElementById("start-goal"),
  stopGoal: document.getElementById("stop-goal"),
  resumeGoal: document.getElementById("resume-goal"),
  deleteGoal: document.getElementById("delete-goal"),
  goalbar: document.getElementById("goalbar"),
  goalTitle: document.getElementById("goal-title"),
  goalStatus: document.getElementById("goal-status"),
  goalProgressLabel: document.getElementById("goal-progress-label"),
  goalProgressBar: document.getElementById("goal-progress-bar"),
  goalForm: document.getElementById("goal-form"),
  goalInputTitle: document.getElementById("goal-input-title"),
  goalInputDescription: document.getElementById("goal-input-description"),
  goalInputRecipe: document.getElementById("goal-input-recipe"),
  goalInputReviewRounds: document.getElementById("goal-input-review-rounds"),
  composerForm: document.getElementById("composer-form"),
  sendMode: document.getElementById("send-mode"),
  targetAgent: document.getElementById("target-agent"),
  reviewAgent: document.getElementById("review-agent"),
  taskTitle: document.getElementById("task-title"),
  dependsOn: document.getElementById("depends-on"),
  composerInput: document.getElementById("composer-input"),
  agentsList: document.getElementById("agents-list"),
  tasksList: document.getElementById("tasks-list"),
  messageList: document.getElementById("message-list"),
  searchInput: document.getElementById("search-input"),
  agentFilter: document.getElementById("agent-filter"),
  messageTypes: document.getElementById("message-types"),
  clearMessages: document.getElementById("clear-messages"),
  clearTasks: document.getElementById("clear-tasks"),
  brainTrigger: document.getElementById("brain-trigger"),
  brainInvocations: document.getElementById("brain-invocations"),
  workflowStatus: document.getElementById("workflow-status"),
  brainProcess: document.getElementById("brain-process"),
  brainThinking: document.getElementById("brain-thinking"),
  brainDecisions: document.getElementById("brain-decisions"),
  planView: document.getElementById("plan-view"),
  planEditor: document.getElementById("plan-editor"),
  togglePlanEditor: document.getElementById("toggle-plan-editor"),
  planForm: document.getElementById("plan-form"),
  planInput: document.getElementById("plan-input"),
  humanInputPanel: document.getElementById("human-input-panel"),
  tabTasksCount: document.getElementById("tab-tasks-count"),
  toastContainer: document.getElementById("toast-container"),
  themeToggle: document.getElementById("theme-toggle"),
  workflowStepper: document.getElementById("workflow-stepper"),
  goalHistoryToggle: document.getElementById("goal-history-toggle"),
  goalHistoryDropdown: document.getElementById("goal-history-dropdown"),
  diffVersionA: document.getElementById("diff-version-a"),
  diffVersionB: document.getElementById("diff-version-b"),
  loadDiff: document.getElementById("load-diff"),
  diffView: document.getElementById("diff-view"),
  gatePanel: document.getElementById("gate-panel"),
  gateTitle: document.getElementById("gate-title"),
  gateMessage: document.getElementById("gate-message"),
  gateFeedback: document.getElementById("gate-feedback"),
  gateApprove: document.getElementById("gate-approve"),
  gateReject: document.getElementById("gate-reject"),
};

const messageTypes = [
  "human\u2192coordinator",
  "coordinator\u2192agent",
  "agent\u2192coordinator",
  "agent\u2192agent",
  "coordinator\u2192human",
  "system",
];

let socket;
let reconnectDelay = 1000;

/* ---- Toast Notification System ---- */

function showToast(message, type) {
  type = type || "info";
  const toast = document.createElement("div");
  toast.className = `toast toast-${type}`;
  toast.textContent = message;
  refs.toastContainer.appendChild(toast);
  window.setTimeout(() => {
    toast.classList.add("toast-out");
    toast.addEventListener("animationend", () => toast.remove());
  }, 4000);
}

/* ---- Reject Modal ---- */

function showRejectModal(taskId) {
  const overlay = document.createElement("div");
  overlay.className = "reject-modal-overlay";
  const modal = document.createElement("div");
  modal.className = "reject-modal";
  const heading = document.createElement("h3");
  heading.textContent = "Reject Task";
  const textarea = document.createElement("textarea");
  textarea.rows = 3;
  textarea.placeholder = "Reason for rejection";
  const actions = document.createElement("div");
  actions.className = "reject-modal-actions";
  const cancelBtn = document.createElement("button");
  cancelBtn.type = "button";
  cancelBtn.className = "btn-sm";
  cancelBtn.textContent = "Cancel";
  cancelBtn.addEventListener("click", () => overlay.remove());
  const submitBtn = document.createElement("button");
  submitBtn.type = "button";
  submitBtn.className = "btn-sm btn-primary";
  submitBtn.textContent = "Reject";
  submitBtn.addEventListener("click", () => {
    const reason = textarea.value.trim();
    if (reason) {
      apiPost(`/api/tasks/${taskId}/reject`, { reason });
      overlay.remove();
    }
  });
  actions.append(cancelBtn, submitBtn);
  modal.append(heading, textarea, actions);
  overlay.appendChild(modal);
  overlay.addEventListener("click", (e) => { if (e.target === overlay) overlay.remove(); });
  document.body.appendChild(overlay);
  textarea.focus();
}

/* ---- File Viewer ---- */

async function openFileViewer(filePath) {
  try {
    const response = await fetch(`/api/workspace/files/${encodeURIComponent(filePath)}`);
    if (!response.ok) {
      showToast(`Failed to load file: ${response.status}`, "error");
      return;
    }
    const content = await response.text();
    const overlay = document.createElement("div");
    overlay.className = "file-viewer-overlay";
    const panel = document.createElement("div");
    panel.className = "file-viewer-panel";
    const header = document.createElement("div");
    header.className = "file-viewer-header";
    const title = document.createElement("span");
    title.className = "file-viewer-title";
    title.textContent = filePath;
    const closeBtn = document.createElement("button");
    closeBtn.type = "button";
    closeBtn.className = "file-viewer-close";
    closeBtn.textContent = "Close";
    closeBtn.addEventListener("click", () => overlay.remove());
    header.append(title, closeBtn);
    const body = document.createElement("div");
    body.className = "file-viewer-content";
    const pre = document.createElement("pre");
    pre.textContent = content;
    body.appendChild(pre);
    panel.append(header, body);
    overlay.appendChild(panel);
    overlay.addEventListener("click", (e) => { if (e.target === overlay) overlay.remove(); });
    document.body.appendChild(overlay);
  } catch (err) {
    showToast(`Error loading file: ${err.message}`, "error");
  }
}

/* ---- Theme Toggle ---- */

function initTheme() {
  const saved = localStorage.getItem("agentbridge-theme");
  if (saved === "light") {
    document.body.classList.add("light-theme");
  }
  refs.themeToggle.addEventListener("click", () => {
    document.body.classList.toggle("light-theme");
    const isLight = document.body.classList.contains("light-theme");
    localStorage.setItem("agentbridge-theme", isLight ? "light" : "dark");
  });
}

/* ---- Collapsible Content Helper ---- */

function makeCollapsible(container, pre) {
  container.classList.add("collapsible-content");
  const toggle = document.createElement("button");
  toggle.type = "button";
  toggle.className = "collapsible-toggle";
  toggle.textContent = "Show more";
  toggle.addEventListener("click", () => {
    const expanded = container.classList.toggle("expanded");
    toggle.textContent = expanded ? "Show less" : "Show more";
  });
  // Only add toggle if content actually overflows 3 lines
  requestAnimationFrame(() => {
    if (pre.scrollHeight > container.clientHeight + 2) {
      container.parentNode.insertBefore(toggle, container.nextSibling);
    } else {
      container.classList.remove("collapsible-content");
    }
  });
}

/* ---- Verdict Badge Helper ---- */

function extractVerdict(text) {
  if (!text) return null;
  const match = text.match(/VERDICT:\s*(PASS|FAIL)/i);
  return match ? match[1].toUpperCase() : null;
}

function verdictBadge(verdict) {
  const badge = document.createElement("span");
  badge.className = `verdict-badge verdict-${verdict.toLowerCase()}`;
  badge.textContent = verdict;
  return badge;
}

/* ---- Workflow Stepper ---- */

function renderWorkflowStepper() {
  refs.workflowStepper.textContent = "";
  const recipe = state.workflow.recipe;
  if (recipe !== "spec-review-loop" && recipe !== "spec-cross-critique-loop") return;

  let stages;
  if (recipe === "spec-cross-critique-loop") {
    stages = ["prepare_spec", "adversarial_review", "cross_critique", "consolidation"];
  } else {
    stages = ["prepare_spec", "adversarial_review", "consolidation"];
  }

  const currentStage = state.workflow.stage || "";

  const stageIdx = stages.indexOf(currentStage);
  stages.forEach((stageKey, idx) => {
    if (idx > 0) {
      const connector = document.createElement("span");
      connector.className = "stepper-connector";
      if (stageIdx >= 0 && idx <= stageIdx) connector.classList.add("completed");
      refs.workflowStepper.appendChild(connector);
    }
    const step = document.createElement("span");
    step.className = "stepper-step";
    if (stageKey === currentStage) {
      step.classList.add("active");
    } else if (stageIdx >= 0 && idx < stageIdx) {
      step.classList.add("completed");
    }
    const label = document.createElement("span");
    label.className = "step-label";
    label.textContent = stageKey.replaceAll("_", " ");
    step.appendChild(label);
    refs.workflowStepper.appendChild(step);
  });

  // Round indicator
  const round = state.workflow.review_round || 0;
  const maxRounds = state.workflow.max_review_rounds || (state.currentGoal ? state.currentGoal.max_review_rounds : 0) || 0;
  if (round > 0 || maxRounds > 0) {
    const roundEl = document.createElement("span");
    roundEl.className = "stepper-round";
    roundEl.textContent = maxRounds ? `Round ${round}/${maxRounds}` : `Round ${round}`;
    refs.workflowStepper.appendChild(roundEl);
  }
}

/* ---- Goal History ---- */

function renderGoalHistory() {
  refs.goalHistoryDropdown.textContent = "";
  if (!state.goals.length) return;

  state.goals.slice().reverse().forEach((goal) => {
    const item = document.createElement("div");
    item.className = "goal-history-item";
    if (state.currentGoal && goal.id === state.currentGoal.id) {
      item.classList.add("current");
    }
    const titleEl = document.createElement("span");
    titleEl.className = "goal-history-title";
    titleEl.textContent = goal.title || "(untitled)";
    const statusEl = document.createElement("span");
    statusEl.className = `tag ${goalStatusTagClass(goal)}`;
    statusEl.textContent = effectiveGoalStatus(goal);
    item.append(titleEl, statusEl);
    refs.goalHistoryDropdown.appendChild(item);
  });
}

function goalStatusTagClass(goal) {
  const status = effectiveGoalStatus(goal);
  if (status === "active" || status === "planning") return "tag-accent";
  if (status === "completed" || status === "finished") return "tag-success";
  if (status === "failed" || status === "blocked") return "tag-danger";
  if (status === "stopped" || status === "gated") return "tag-warning";
  return "";
}

/* ---- Spec Diff ---- */

function renderDiffVersionOptions() {
  const pattern = /^specs\/.*-spec-v(\d+)\.md$/;
  const versions = [];
  (state.workspaceFiles || []).forEach((filePath) => {
    const match = filePath.match(pattern);
    if (match) versions.push(Number.parseInt(match[1], 10));
  });
  versions.sort((a, b) => a - b);
  const unique = [...new Set(versions)];

  [refs.diffVersionA, refs.diffVersionB].forEach((select) => {
    select.textContent = "";
    unique.forEach((v) => {
      const opt = document.createElement("option");
      opt.value = String(v);
      opt.textContent = `v${v}`;
      select.appendChild(opt);
    });
  });

  if (unique.length >= 2) {
    refs.diffVersionA.value = String(unique[unique.length - 2]);
    refs.diffVersionB.value = String(unique[unique.length - 1]);
  }
}

function computeLineDiff(textA, textB) {
  const linesA = textA.split("\n");
  const linesB = textB.split("\n");
  const result = [];

  // LCS-based diff
  const m = linesA.length;
  const n = linesB.length;

  // Build LCS table
  const dp = Array.from({ length: m + 1 }, () => new Array(n + 1).fill(0));
  for (let i = 1; i <= m; i++) {
    for (let j = 1; j <= n; j++) {
      if (linesA[i - 1] === linesB[j - 1]) {
        dp[i][j] = dp[i - 1][j - 1] + 1;
      } else {
        dp[i][j] = Math.max(dp[i - 1][j], dp[i][j - 1]);
      }
    }
  }

  // Backtrack to produce diff
  let i = m;
  let j = n;
  const stack = [];
  while (i > 0 || j > 0) {
    if (i > 0 && j > 0 && linesA[i - 1] === linesB[j - 1]) {
      stack.push({ type: "same", text: linesA[i - 1] });
      i--;
      j--;
    } else if (j > 0 && (i === 0 || dp[i][j - 1] >= dp[i - 1][j])) {
      stack.push({ type: "add", text: linesB[j - 1] });
      j--;
    } else {
      stack.push({ type: "remove", text: linesA[i - 1] });
      i--;
    }
  }
  stack.reverse();
  return stack;
}

async function loadSpecDiff() {
  const vA = refs.diffVersionA.value;
  const vB = refs.diffVersionB.value;
  if (!vA || !vB) {
    showToast("Select two versions to compare.", "error");
    return;
  }

  // Find matching files for each version
  const pattern = /^specs\/.*-spec-v(\d+)\.md$/;
  const filesA = (state.workspaceFiles || []).filter((f) => {
    const m = f.match(pattern);
    return m && m[1] === vA;
  });
  const filesB = (state.workspaceFiles || []).filter((f) => {
    const m = f.match(pattern);
    return m && m[1] === vB;
  });

  if (!filesA.length || !filesB.length) {
    showToast("Could not find spec files for selected versions.", "error");
    return;
  }

  try {
    const [respA, respB] = await Promise.all([
      fetch(`/api/workspace/files/${encodeURIComponent(filesA[0])}`),
      fetch(`/api/workspace/files/${encodeURIComponent(filesB[0])}`),
    ]);
    if (!respA.ok || !respB.ok) {
      showToast("Failed to fetch spec files.", "error");
      return;
    }
    const [textA, textB] = await Promise.all([respA.text(), respB.text()]);
    const diff = computeLineDiff(textA, textB);
    renderDiff(diff);
  } catch (err) {
    showToast(`Error loading diff: ${err.message}`, "error");
  }
}

function renderDiff(diffLines) {
  refs.diffView.textContent = "";
  if (!diffLines.length) {
    const empty = document.createElement("p");
    empty.className = "empty-state";
    empty.textContent = "No differences found.";
    refs.diffView.appendChild(empty);
    return;
  }

  diffLines.forEach((line) => {
    const el = document.createElement("div");
    el.className = "diff-line";
    if (line.type === "add") {
      el.classList.add("diff-add");
      el.textContent = `+ ${line.text}`;
    } else if (line.type === "remove") {
      el.classList.add("diff-remove");
      el.textContent = `- ${line.text}`;
    } else {
      el.classList.add("diff-same");
      el.textContent = `  ${line.text}`;
    }
    refs.diffView.appendChild(el);
  });
}

/* ---- Gate Resolution ---- */

async function approveGate() {
  const goal = state.currentGoal;
  if (!goal) return;
  const response = await apiPost(`/api/goals/${goal.id}/gate`, {
    approved: true,
    feedback: refs.gateFeedback.value,
  });
  if (response !== false) {
    showToast("Gate approved", "success");
    refs.gateFeedback.value = "";
  }
}

async function rejectGate() {
  const goal = state.currentGoal;
  if (!goal) return;
  const response = await apiPost(`/api/goals/${goal.id}/gate`, {
    approved: false,
    feedback: refs.gateFeedback.value,
  });
  if (response !== false) {
    showToast("Gate rejected", "info");
    refs.gateFeedback.value = "";
  }
}

/* ---- Resolve Dependency UUIDs ---- */

function resolveTaskTitle(taskId) {
  const task = state.tasks.find((t) => t.id === taskId);
  return task ? task.title : taskId.slice(0, 8);
}

/* ---- Tabs ---- */

function initTabs() {
  const tabs = document.querySelectorAll(".sidebar-link[data-tab]");
  tabs.forEach((tab) => {
    tab.addEventListener("click", () => {
      tabs.forEach((entry) => entry.classList.remove("active"));
      tab.classList.add("active");
      document.querySelectorAll(".tab-panel[data-tab]").forEach((panel) => {
        panel.classList.toggle("active", panel.dataset.tab === tab.dataset.tab);
      });
    });
  });
}

function switchToTab(tabName) {
  const tabs = document.querySelectorAll(".sidebar-link[data-tab]");
  tabs.forEach((tab) => {
    tab.classList.toggle("active", tab.dataset.tab === tabName);
  });
  document.querySelectorAll(".tab-panel[data-tab]").forEach((panel) => {
    panel.classList.toggle("active", panel.dataset.tab === tabName);
  });
}

function init() {
  initTabs();
  initTheme();

  messageTypes.forEach((type) => {
    const chip = document.createElement("button");
    chip.className = "chip active";
    chip.type = "button";
    chip.textContent = type;
    state.messageTypeFilter.add(type);
    chip.addEventListener("click", () => {
      if (state.messageTypeFilter.has(type)) {
        state.messageTypeFilter.delete(type);
        chip.classList.remove("active");
      } else {
        state.messageTypeFilter.add(type);
        chip.classList.add("active");
      }
      renderMessages();
    });
    refs.messageTypes.appendChild(chip);
  });

  refs.searchInput.addEventListener("input", (event) => {
    state.search = event.target.value.toLowerCase();
    renderMessages();
  });
  refs.agentFilter.addEventListener("change", (event) => {
    state.agentFilter = event.target.value;
    renderAgents();
    renderMessages();
  });
  refs.clearMessages.addEventListener("click", () => apiPost("/api/messages/clear"));
  refs.clearTasks.addEventListener("click", () => apiPost("/api/tasks/clear"));
  refs.startGoal.addEventListener("click", startGoal);
  refs.stopGoal.addEventListener("click", stopGoal);
  refs.resumeGoal.addEventListener("click", resumeGoal);
  refs.deleteGoal.addEventListener("click", deleteGoal);
  refs.refreshWorkspace.addEventListener("click", refreshWorkspace);
  refs.uploadWorkspace.addEventListener("click", uploadWorkspaceFiles);
  refs.goalForm.addEventListener("submit", submitGoal);
  refs.composerForm.addEventListener("submit", submitComposer);
  refs.sendMode.addEventListener("change", toggleComposerFields);
  refs.togglePlanEditor.addEventListener("click", togglePlanEditor);
  refs.planForm.addEventListener("submit", submitPlanOverride);

  // Goal history toggle
  refs.goalHistoryToggle.addEventListener("click", () => {
    state.goalHistoryOpen = !state.goalHistoryOpen;
    refs.goalHistoryDropdown.classList.toggle("hidden", !state.goalHistoryOpen);
    if (state.goalHistoryOpen) renderGoalHistory();
  });
  document.addEventListener("click", (e) => {
    if (state.goalHistoryOpen && !refs.goalHistoryToggle.contains(e.target) && !refs.goalHistoryDropdown.contains(e.target)) {
      state.goalHistoryOpen = false;
      refs.goalHistoryDropdown.classList.add("hidden");
    }
  });

  refs.loadDiff.addEventListener("click", loadSpecDiff);
  refs.gateApprove.addEventListener("click", approveGate);
  refs.gateReject.addEventListener("click", rejectGate);

  toggleComposerFields();
  renderHumanInputRequest();
  connectWebSocket();
  window.setInterval(() => {
    updateAgentTimers();
    renderWorkflowPanel();
    updateTaskTimers();
  }, 1000);
}

function connectWebSocket() {
  const protocol = window.location.protocol === "https:" ? "wss" : "ws";
  socket = new WebSocket(`${protocol}://${window.location.host}/ws`);

  socket.addEventListener("open", () => {
    refs.connectionDot.className = "topbar-dot connected";
    refs.connectionLabel.textContent = "connected";
    reconnectDelay = 1000;
  });

  socket.addEventListener("message", (event) => {
    const payload = JSON.parse(event.data);
    switch (payload.event) {
      case "snapshot":
        hydrateState(payload.data);
        renderAll();
        break;
      case "message":
        state.messages.push(payload.data);
        renderMessages();
        renderWorkflowPanel();
        break;
      case "task_update":
        upsertTask(payload.data);
        requestTaskRender();
        renderPlan();
        renderGoalBar();
        renderWorkflowPanel();
        refreshWorkspace();
        break;
      case "agent_status":
        state.agents[payload.data.name] = payload.data;
        renderAgents();
        populateAgentOptions();
        renderWorkflowPanel();
        break;
      case "goal_update":
        upsertGoal(payload.data);
        renderGoalBar();
        renderWorkflowPanel();
        break;
      case "plan_update":
        state.plan = payload.data;
        renderGoalBar();
        renderPlan();
        renderWorkflowPanel();
        break;
      case "workflow_update":
        state.workflow = payload.data || {};
        populateAgentOptions();
        renderGoalBar();
        renderWorkflowPanel();
        break;
    }
  });

  socket.addEventListener("close", () => {
    refs.connectionDot.className = "topbar-dot reconnecting";
    refs.connectionLabel.textContent = "reconnecting";
    window.setTimeout(connectWebSocket, reconnectDelay);
    reconnectDelay = Math.min(reconnectDelay * 2, 30000);
  });
}

function hydrateState(data) {
  state.agents = data.agents || {};
  state.tasks = data.tasks || [];
  state.goals = data.goals || [];
  state.currentGoal = data.current_goal || chooseCurrentGoal();
  state.plan = data.plan || null;
  state.workflow = data.workflow || {};
  state.workspace = data.workspace || {};
  state.messages = data.messages || [];
  state.workspaceFiles = [];
}

function renderAll() {
  populateAgentOptions();
  toggleComposerFields();
  renderGoalBar();
  renderHumanInputRequest();
  renderWorkflowPanel();
  renderPlan();
  renderAgents();
  renderTasks();
  renderMessages();
  renderWorkspace();
  renderDiffVersionOptions();
  refreshWorkspace();

  // Default tab: switch to Launch if no active goal
  if (!state.currentGoal) {
    switchToTab("controls");
  }
}

function beginTaskInteraction(taskId) {
  state.activeTaskInteractionId = taskId;
}

function endTaskInteraction(taskId) {
  if (state.activeTaskInteractionId && state.activeTaskInteractionId !== taskId) return;
  state.activeTaskInteractionId = "";
  if (state.pendingTaskRender) {
    state.pendingTaskRender = false;
    renderTasks();
  }
}

function requestTaskRender() {
  if (state.activeTaskInteractionId) {
    state.pendingTaskRender = true;
    return;
  }
  state.pendingTaskRender = false;
  renderTasks();
}

function updateTaskTimers() {
  refs.tasksList.querySelectorAll("[data-task-timestamp]").forEach((node) => {
    const timestamp = node.dataset.taskTimestamp;
    if (!timestamp) return;
    node.textContent = node.dataset.taskRunning === "true"
      ? elapsed(timestamp)
      : formatTime(timestamp);
  });
}

/* ---- Targeted agent timer updates (change #10) ---- */

function updateAgentTimers() {
  refs.agentsList.querySelectorAll("[data-agent-timer]").forEach((node) => {
    const agentName = node.dataset.agentTimer;
    const activeTask = currentTaskForAgent(agentName);
    if (activeTask) {
      node.textContent = `task: ${activeTask.title} \u2022 ${elapsed(activeTask.started_at || activeTask.created_at)}`;
    } else {
      node.textContent = "no active task";
    }
  });
  refs.agentsList.querySelectorAll("[data-agent-process]").forEach((node) => {
    const agentName = node.dataset.agentProcess;
    const agent = state.agents[agentName];
    if (agent) node.textContent = formatAgentProcess(agent);
  });
}

function populateAgentOptions() {
  const names = Object.keys(state.agents).sort();
  [refs.agentFilter, refs.targetAgent, refs.reviewAgent].forEach((select, index) => {
    const existing = select.value;
    select.textContent = "";
    if (index === 0) {
      appendOption(select, "", "All agents");
    } else if (index === 1) {
      appendOption(select, "", "Select agent");
    } else {
      appendOption(select, "", "No reviewer");
    }
    names.forEach((name) => appendOption(select, name, name));
    if (existing && [...select.options].some((option) => option.value === existing)) {
      select.value = existing;
    }
  });

  const label = state.workflow.recipe || state.workflow.mode || "deterministic";
  refs.brainProviderLabel.textContent = label;
}

function appendOption(select, value, label) {
  const option = document.createElement("option");
  option.value = value;
  option.textContent = label;
  select.appendChild(option);
}

function renderGoalBar() {
  const goal = state.currentGoal;
  if (!goal) {
    refs.goalbar.classList.add("empty");
    refs.goalTitle.textContent = "No active goal";
    refs.goalStatus.textContent = "";
    refs.goalProgressLabel.textContent = "0 / 0";
    refs.goalProgressBar.style.width = "0%";
    refs.startGoal.classList.add("hidden");
    refs.stopGoal.classList.add("hidden");
    refs.resumeGoal.classList.add("hidden");
    refs.deleteGoal.classList.add("hidden");
    refs.goalHistoryToggle.classList.add("hidden");
    refs.workflowStepper.textContent = "";
    refs.gatePanel.classList.add("hidden");
    return;
  }

  refs.goalbar.classList.remove("empty");
  const goalTasks = state.tasks.filter((task) => task.goal_id === goal.id);
  const completed = goalTasks.filter((task) => task.status === "completed").length;
  const total = goalTasks.length;
  const percent = total === 0 ? 0 : Math.round((completed / total) * 100);
  const displayStatus = effectiveGoalStatus(goal);

  refs.goalTitle.textContent = goal.title;

  // Build simplified status text (stepper handles the stage info now)
  const parts = [displayStatus];
  if (state.workflow.recipe !== "spec-review-loop" && state.workflow.recipe !== "spec-cross-critique-loop") {
    if (state.workflow.stage) parts.push(state.workflow.stage.replaceAll("_", " "));
    if (goal.workflow_recipe) parts.push(formatRecipeLabel(goal.workflow_recipe));
    if (state.workflow.current_phase_number) {
      const phaseLabel = state.workflow.total_phases
        ? `phase ${state.workflow.current_phase_number}/${state.workflow.total_phases}`
        : `phase ${state.workflow.current_phase_number}`;
      parts.push(state.workflow.current_phase_title ? `${phaseLabel} ${state.workflow.current_phase_title}` : phaseLabel);
    }
  } else {
    if (goal.workflow_recipe) parts.push(formatRecipeLabel(goal.workflow_recipe));
    if (state.workflow.stage_task_total) {
      let progressLabel = "tasks";
      if (state.workflow.stage === "adversarial_review") progressLabel = "reviewers";
      if (state.workflow.stage === "cross_critique") progressLabel = "critiques";
      parts.push(`${progressLabel} ${state.workflow.stage_task_completed || 0}/${state.workflow.stage_task_total} complete`);
    }
  }
  if (goal.summary && isTerminalGoalStatus(displayStatus)) {
    parts.push(clampText(goal.summary, 220));
  } else if (goal.description) {
    parts.push(clampText(goal.description, 180));
  }

  refs.goalStatus.textContent = parts.filter(Boolean).join(" \u2022 ");
  refs.goalProgressLabel.textContent = `${completed} / ${total}`;
  refs.goalProgressBar.style.width = `${percent}%`;
  refs.startGoal.classList.toggle("hidden", !canStartGoal(goal));
  refs.stopGoal.classList.toggle("hidden", !canStopGoal(goal));
  refs.resumeGoal.classList.toggle("hidden", !canResumeGoal(goal));
  refs.deleteGoal.classList.toggle("hidden", !canDeleteGoal(goal));

  // Show goal history toggle if there are multiple goals
  refs.goalHistoryToggle.classList.toggle("hidden", state.goals.length < 2);

  // Gate panel
  if (goal.active_gate && goal.status === "gated") {
    refs.gatePanel.classList.remove("hidden");
    const gateType = goal.active_gate.type || "Human Decision Required";
    refs.gateTitle.textContent = gateType.replace(/_/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
    refs.gateMessage.textContent = goal.active_gate.message || "";
  } else {
    refs.gatePanel.classList.add("hidden");
  }

  renderWorkflowStepper();
}

function canStartGoal(goal) {
  if (!goal || !state.plan || state.plan.goal_id !== goal.id) return false;
  return goal.status === "stopped" && !isPlanCompleteForGoal(goal.id);
}

function canStopGoal(goal) {
  return Boolean(goal && (goal.status === "planning" || goal.status === "active"));
}

function canResumeGoal(goal) {
  if (!goal || !state.plan || state.plan.goal_id !== goal.id) return false;
  if (!["blocked", "failed", "gated"].includes(goal.status)) return false;
  return !isPlanCompleteForGoal(goal.id);
}

function canDeleteGoal(goal) {
  return Boolean(goal);
}

function effectiveGoalStatus(goal) {
  if (!goal) return "";
  if (isPlanCompleteForGoal(goal.id) && ["blocked", "failed", "stopped", "completed", "gated"].includes(goal.status)) {
    return "finished";
  }
  return goal.status;
}

function isTerminalGoalStatus(status) {
  return ["blocked", "failed", "stopped", "completed", "finished", "gated"].includes(status);
}

function isPlanCompleteForGoal(goalID) {
  if (!state.plan || state.plan.goal_id !== goalID) return true;
  return (state.plan.phases || []).every((phase) => (phase.tasks || []).every((plannedTask) => {
    if (!plannedTask.real_task_id) return false;
    const actual = state.tasks.find((task) => task.id === plannedTask.real_task_id);
    return actual && actual.status === "completed";
  }));
}

function renderWorkspace() {
  refs.workspacePath.textContent = state.workspace.path || "unknown";
  refs.workspaceTree.textContent = "";
  if (!state.workspaceFiles.length) {
    const empty = document.createElement("p");
    empty.className = "empty-state";
    empty.textContent = "No workspace files loaded.";
    refs.workspaceTree.appendChild(empty);
    return;
  }

  buildFileTree(state.workspaceFiles).forEach((node) => {
    refs.workspaceTree.appendChild(renderTreeNode(node));
  });
}

function renderHumanInputRequest() {
  refs.humanInputPanel.classList.add("hidden");
  refs.humanInputPanel.textContent = "";
}

function renderWorkflowPanel() {
  refs.brainTrigger.textContent = state.workflow.stage || "workflow";
  refs.brainInvocations.textContent = formatWorkflowMeta(state.workflow);
  refs.workflowStatus.textContent = formatWorkflowStatus(state.workflow);
  refs.brainProcess.textContent = formatWorkflowProcess(primaryWorkflowInvocation(), state.workflow);
  refs.brainThinking.textContent = formatWorkflowGuidance(state.workflow, state.currentGoal);
  renderTransitions();
}

function renderTransitions() {
  refs.brainDecisions.textContent = "";
  const entries = workflowTransitions();
  if (!entries.length) {
    const empty = document.createElement("p");
    empty.className = "empty-state";
    empty.textContent = "No transitions recorded yet.";
    refs.brainDecisions.appendChild(empty);
    return;
  }

  entries.forEach((message) => {
    const item = document.createElement("article");
    item.className = "decision-item";

    const header = document.createElement("strong");
    header.textContent = `${formatTime(message.timestamp)} \u2022 ${message.content}`;
    item.appendChild(header);

    const meta = [];
    if (message.metadata && message.metadata.goal) meta.push(`goal ${message.metadata.goal.status}`);
    if (message.metadata && message.metadata.task) meta.push(`task ${message.metadata.task.status}`);
    if (message.metadata && message.metadata.plan) meta.push("plan update");
    if (meta.length) {
      const metaEl = document.createElement("div");
      metaEl.className = "card-meta";
      metaEl.textContent = meta.join(" \u2022 ");
      item.appendChild(metaEl);
    }

    refs.brainDecisions.appendChild(item);
  });
}

function workflowTransitions() {
  const goalID = state.currentGoal ? state.currentGoal.id : "";
  return state.messages
    .filter((message) => {
      if (message.type !== "system") return false;
      if (message.from !== "coordinator" && message.from !== "system") return false;
      if (goalID) {
        if (message.metadata && message.metadata.goal && message.metadata.goal.id === goalID) return true;
        if (message.metadata && message.metadata.task && message.metadata.task.goal_id === goalID) return true;
        if (message.metadata && message.metadata.plan && message.metadata.plan.goal_id === goalID) return true;
        return false;
      }
      return Boolean(message.metadata && (message.metadata.goal || message.metadata.plan || message.metadata.task));
    })
    .slice(-8);
}

function primaryWorkflowInvocation() {
  const runningTask = state.tasks.find((task) => task.status === "running");
  if (runningTask) {
    return state.agents[runningTask.assigned_to]?.last_invocation || null;
  }

  const failedTask = state.tasks
    .slice()
    .reverse()
    .find((task) => task.status === "failed" || task.status === "cancelled");
  if (failedTask) {
    return state.agents[failedTask.assigned_to]?.last_invocation || null;
  }

  return null;
}

function formatWorkflowMeta(workflow) {
  if (!workflow || !workflow.mode) return "No active workflow.";
  const parts = [workflow.mode];
  if (workflow.recipe) parts.push(formatRecipeLabel(workflow.recipe));
  if (workflow.review_round) {
    const rounds = workflow.max_review_rounds
      ? `active review round ${workflow.review_round}/${workflow.max_review_rounds}`
      : `active review round ${workflow.review_round}`;
    parts.push(rounds);
  } else if (workflow.max_review_rounds) {
    parts.push(`active review round 0/${workflow.max_review_rounds}`);
  }
  if (workflow.completed_review_rounds) {
    parts.push(`completed rounds ${workflow.completed_review_rounds}`);
  }
  return parts.join(" \u2022 ");
}

function formatRecipeLabel(recipe) {
  switch (recipe) {
    case "spec-cross-critique-loop":
      return "cross-critique recipe";
    case "spec-review-loop":
      return "parallel review recipe";
    default:
      return recipe;
  }
}

function formatWorkflowStatus(workflow) {
  if (!workflow || !workflow.mode) return "No active workflow state.";
  const parts = [workflow.status || "idle"];
  if (workflow.stage) parts.push(workflow.stage.replaceAll("_", " "));
  if ((workflow.recipe === "spec-review-loop" || workflow.recipe === "spec-cross-critique-loop") && workflow.stage_task_total) {
    if (workflow.stage === "adversarial_review") {
      parts.push(`reviewers ${workflow.stage_task_completed || 0}/${workflow.stage_task_total} complete`);
    } else if (workflow.stage === "cross_critique") {
      parts.push(`critiques ${workflow.stage_task_completed || 0}/${workflow.stage_task_total} complete`);
    } else {
      parts.push(`tasks ${workflow.stage_task_completed || 0}/${workflow.stage_task_total} complete`);
    }
  } else if (workflow.current_phase_number) {
    const phaseLabel = workflow.total_phases
      ? `phase ${workflow.current_phase_number}/${workflow.total_phases}`
      : `phase ${workflow.current_phase_number}`;
    parts.push(workflow.current_phase_title ? `${phaseLabel} ${workflow.current_phase_title}` : phaseLabel);
  }
  if (workflow.last_error) parts.push(`error: ${clampText(workflow.last_error, 220)}`);
  return parts.join(" \u2022 ");
}

function formatWorkflowProcess(invocation, workflow) {
  if (!invocation) {
    if (workflow && workflow.status === "stopped" && workflow.last_error) {
      return `Workflow stopped \u2022 ${workflow.last_error}`;
    }
    if (workflow && workflow.status === "blocked" && workflow.last_error) {
      return `Workflow blocked \u2022 ${workflow.last_error}`;
    }
    return "No active worker process telemetry yet.";
  }
  const parts = [invocation.status || "unknown"];
  if (invocation.command) parts.push(invocation.command);
  if (invocation.pid) parts.push(`pid ${invocation.pid}`);
  if (invocation.timeout_seconds) parts.push(`timeout ${invocation.timeout_seconds}s`);
  if (invocation.stdout_bytes || invocation.stderr_bytes) {
    parts.push(`io ${invocation.stdout_bytes || 0}/${invocation.stderr_bytes || 0} bytes`);
  }
  if (invocation.trace_id) parts.push(`trace ${String(invocation.trace_id).slice(0, 12)}`);
  if (invocation.error) parts.push(invocation.error);
  return parts.join(" \u2022 ");
}

function formatWorkflowGuidance(workflow, goal) {
  if (!goal) {
    return "Submit a goal to start the deterministic spec workflow.";
  }
  const lines = [];
  lines.push(`Recipe: ${workflow.recipe || workflow.mode || "deterministic"}`);
  if (workflow.stage) lines.push(`Stage: ${workflow.stage.replaceAll("_", " ")}`);
  if (workflow.stage_detail) lines.push(workflow.stage_detail);
  if (goal.summary && (goal.status === "blocked" || goal.status === "failed" || goal.status === "stopped")) {
    lines.push(`Failure: ${clampText(goal.summary, 320)}`);
  }
  if (workflow.last_error && workflow.last_error !== goal.summary) {
    lines.push(`Coordinator note: ${clampText(workflow.last_error, 320)}`);
  }
  return lines.join("\n\n");
}

function renderPlan() {
  refs.planView.textContent = "";
  refs.planInput.value = state.plan ? JSON.stringify(state.plan, null, 2) : "";
  if (!state.plan || !state.plan.phases || state.plan.phases.length === 0) {
    const empty = document.createElement("p");
    empty.className = "empty-state";
    empty.textContent = "No active plan.";
    refs.planView.appendChild(empty);
    return;
  }

  const grid = document.createElement("div");
  grid.className = "plan-grid";
  const plannedTaskLookup = buildPlannedTaskLookup(state.plan);

  state.plan.phases.forEach((phase) => {
    const phaseCard = document.createElement("article");
    phaseCard.className = "phase-card";

    const header = document.createElement("div");
    header.className = "phase-card-header";
    const titleWrap = document.createElement("div");
    const title = document.createElement("div");
    title.className = "phase-card-title";
    title.textContent = `Phase ${phase.number}: ${phase.title}`;
    titleWrap.appendChild(title);
    if (phase.description) {
      const desc = document.createElement("div");
      desc.className = "phase-card-desc";
      desc.textContent = phase.description;
      titleWrap.appendChild(desc);
    }
    const statusTag = document.createElement("span");
    statusTag.className = `tag ${phaseStatusTagClass(phase)}`;
    statusTag.textContent = phaseStatus(phase);
    header.append(titleWrap, statusTag);

    const tasks = document.createElement("div");
    tasks.className = "phase-tasks";
    (phase.tasks || []).forEach((plannedTask) => {
      const actual = state.tasks.find((task) => task.id === plannedTask.real_task_id);
      const taskEl = document.createElement("div");
      taskEl.className = `phase-task${actual && actual.status === "completed" ? " completed" : ""}`;

      const taskHeader = document.createElement("div");
      taskHeader.className = "phase-task-header";
      const taskTitle = document.createElement("span");
      taskTitle.className = "phase-task-title";
      taskTitle.textContent = plannedTask.title;
      const taskTag = document.createElement("span");
      taskTag.className = "tag";
      taskTag.textContent = actual ? `${actual.assigned_to} \u2022 ${actual.status}` : `${plannedTask.assign_to} \u2022 planned`;
      taskHeader.append(taskTitle, taskTag);

      const meta = document.createElement("div");
      meta.className = "tags";
      meta.style.marginTop = "4px";
      meta.append(
        tag(`p${plannedTask.priority || 3}`),
        tag(plannedTask.review_by ? `review: ${plannedTask.review_by}` : "no review"),
      );

      // Verdict badge for plan tasks
      if (actual && actual.result) {
        const verdict = extractVerdict(actual.result);
        if (verdict) meta.appendChild(verdictBadge(verdict));
      }

      const desc = document.createElement("pre");
      desc.textContent = plannedTask.description;

      const deps = document.createElement("div");
      deps.className = "card-meta";
      deps.textContent = plannedTask.depends_on && plannedTask.depends_on.length
        ? `depends on ${resolvePlanDependencyLabels(plannedTask.depends_on, plannedTaskLookup).join(", ")}`
        : "no dependencies";

      const fileRefs = document.createElement("div");
      fileRefs.className = "card-meta";
      const referenceFiles = actual
        ? combineReferenceList(actual.discussion_file, actual.files_touched, actual.files_changed)
        : [];
      fileRefs.textContent = referenceFiles.length ? `files ${referenceFiles.join(", ")}` : "no file refs yet";

      taskEl.append(taskHeader, meta, desc, deps, fileRefs);
      tasks.appendChild(taskEl);
    });

    phaseCard.append(header, tasks);
    grid.appendChild(phaseCard);
  });

  refs.planView.appendChild(grid);
}

function buildPlannedTaskLookup(plan) {
  const lookup = new Map();
  if (!plan || !plan.phases) return lookup;
  plan.phases.forEach((phase) => {
    (phase.tasks || []).forEach((task) => {
      if (task.temp_id) lookup.set(task.temp_id, task);
    });
  });
  return lookup;
}

function resolvePlanDependencyLabels(dependsOn, plannedTaskLookup) {
  return (dependsOn || []).map((dep) => {
    const planned = plannedTaskLookup.get(dep);
    if (!planned) return dep;
    const actual = planned.real_task_id
      ? state.tasks.find((task) => task.id === planned.real_task_id)
      : null;
    if (actual) return actual.title;
    return planned.title || dep;
  });
}

function combineReferenceList(...groups) {
  const values = new Set();
  groups.flat().forEach((entry) => {
    if (typeof entry === "string" && entry.trim()) values.add(entry.trim());
  });
  return Array.from(values);
}

function clampText(value, limit) {
  if (!value || value.length <= limit) return value;
  return `${value.slice(0, limit - 1)}\u2026`;
}

function renderAgents() {
  refs.agentsList.textContent = "";
  const agentList = Object.values(state.agents).sort((a, b) => a.name.localeCompare(b.name));

  if (!agentList.length) {
    const empty = document.createElement("p");
    empty.className = "empty-state";
    empty.textContent = "No agents connected.";
    refs.agentsList.appendChild(empty);
    return;
  }

  agentList.forEach((agent) => {
    const card = document.createElement("article");
    card.className = "card agent-card";
    if (state.agentFilter === agent.name) card.classList.add("active-filter");

    const header = document.createElement("div");
    header.className = "card-header";
    const left = document.createElement("div");
    const name = document.createElement("span");
    name.className = "card-title";
    name.textContent = agent.name;
    const tags = document.createElement("div");
    tags.className = "tags";
    tags.style.marginTop = "4px";
    tags.append(tag(agent.provider || "unknown"), tag(agent.role || "unknown"), statusTag(agent.status));
    const activeTask = currentTaskForAgent(agent.name);
    if (activeTask && activeTask.revision_count) {
      tags.appendChild(tag(`rev ${activeTask.revision_count}`));
    }
    left.append(name, tags);

    const dot = document.createElement("span");
    dot.className = `dot dot-${agent.status}`;
    header.append(left, dot);

    // Use data attributes for targeted timer updates
    const detail = document.createElement("div");
    detail.className = "card-meta";
    detail.dataset.agentTimer = agent.name;
    detail.textContent = activeTask
      ? `task: ${activeTask.title} \u2022 ${elapsed(activeTask.started_at || activeTask.created_at)}`
      : "no active task";

    const process = document.createElement("div");
    process.className = "card-meta";
    process.dataset.agentProcess = agent.name;
    process.textContent = formatAgentProcess(agent);

    const stats = document.createElement("div");
    stats.className = "card-meta";
    stats.textContent = `completed ${agent.tasks_completed || 0} \u2022 failed ${agent.tasks_failed || 0} \u2022 tokens ${agent.total_tokens_in || 0}/${agent.total_tokens_out || 0}`;

    const actions = document.createElement("div");
    actions.className = "card-actions";

    const filter = document.createElement("button");
    filter.type = "button";
    filter.className = "btn-sm";
    filter.textContent = state.agentFilter === agent.name ? "Show All" : "Filter";
    filter.addEventListener("click", () => {
      state.agentFilter = state.agentFilter === agent.name ? "" : agent.name;
      refs.agentFilter.value = state.agentFilter;
      renderAgents();
      renderMessages();
    });
    actions.appendChild(filter);

    if (agent.status === "paused") {
      actions.appendChild(actionButton("Resume", () => apiPost(`/api/agents/${agent.name}/resume`)));
    } else if (agent.status !== "busy") {
      actions.appendChild(actionButton("Pause", () => apiPost(`/api/agents/${agent.name}/pause`)));
    }
    actions.appendChild(actionButton("Reset", () => apiPost(`/api/agents/${agent.name}/reset`)));

    card.append(header, detail, process, stats, actions);
    refs.agentsList.appendChild(card);
  });
}

function formatAgentProcess(agent) {
  const invocation = agent.last_invocation;
  if (!invocation) return "no worker telemetry yet";
  const parts = [invocation.status || "unknown"];
  if (invocation.pid) parts.push(`pid ${invocation.pid}`);
  if (invocation.timeout_seconds) parts.push(`timeout ${invocation.timeout_seconds}s`);
  if (invocation.stdout_bytes || invocation.stderr_bytes) {
    parts.push(`io ${invocation.stdout_bytes || 0}/${invocation.stderr_bytes || 0}`);
  }
  if (invocation.error) {
    parts.push(invocation.error);
  } else if (invocation.last_event_at) {
    parts.push(`last activity ${elapsed(invocation.last_event_at)}`);
  }
  return parts.join(" \u2022 ");
}

function renderTasks() {
  refs.tasksList.textContent = "";

  const groups = {
    running: [],
    pending: [],
    review: [],
    completed: [],
    failed: [],
    cancelled: [],
  };

  state.tasks.forEach((task) => {
    const status = task.status === "blocked" ? "pending" : task.status;
    if (groups[status]) groups[status].push(task);
  });

  const activeCount = groups.running.length + groups.pending.length + groups.review.length;
  refs.tabTasksCount.textContent = activeCount > 0 ? `${activeCount}` : "";

  let hasAny = false;
  Object.entries(groups).forEach(([status, tasks]) => {
    if (!tasks.length) return;
    hasAny = true;

    const group = document.createElement("section");
    group.className = "task-group";
    const head = document.createElement("div");
    head.className = "group-title";
    head.innerHTML = `${status} <span class="group-count">${tasks.length}</span>`;
    group.appendChild(head);

    tasks
      .slice()
      .sort((a, b) => (a.priority || 3) - (b.priority || 3) || Date.parse(a.created_at) - Date.parse(b.created_at))
      .forEach((task) => {
        group.appendChild(renderTaskCard(task));
      });

    refs.tasksList.appendChild(group);
  });

  if (!hasAny) {
    const empty = document.createElement("p");
    empty.className = "empty-state";
    empty.textContent = "No tasks.";
    refs.tasksList.appendChild(empty);
  }
}

function renderTaskCard(task) {
  const card = document.createElement("article");
  card.className = `card status-${task.status}`;

  const header = document.createElement("div");
  header.className = "card-header";
  const titleWrap = document.createElement("div");
  const titleRow = document.createElement("div");
  titleRow.style.display = "flex";
  titleRow.style.alignItems = "center";
  titleRow.style.gap = "8px";
  const priorityDot = document.createElement("span");
  priorityDot.className = `priority-dot p${task.priority || 3}`;
  const title = document.createElement("span");
  title.className = "card-title";
  title.textContent = task.title;
  titleRow.append(priorityDot, title);

  // Verdict badge in header
  const verdict = extractVerdict(task.result);

  const tags = document.createElement("div");
  tags.className = "tags";
  tags.style.marginTop = "4px";
  tags.append(
    tag(task.assigned_to),
    statusTag(task.status),
    tag(`p${task.priority || 3}`),
    task.goal_id ? tag(`goal ${task.goal_id.slice(0, 8)}`) : tag("manual"),
  );
  if (verdict) tags.appendChild(verdictBadge(verdict));

  titleWrap.append(titleRow, tags);
  const timer = document.createElement("span");
  timer.className = "card-meta";
  timer.style.marginTop = "0";
  timer.dataset.taskTimestamp = task.started_at || task.created_at || "";
  timer.dataset.taskRunning = task.status === "running" ? "true" : "false";
  timer.textContent = task.status === "running" ? elapsed(task.started_at || task.created_at) : formatTime(task.created_at);
  header.append(titleWrap, timer);

  // Collapsible description
  const desc = document.createElement("div");
  desc.className = "card-body";
  const descPre = document.createElement("pre");
  descPre.textContent = task.description;
  desc.appendChild(descPre);
  card.append(header, desc);
  makeCollapsible(desc, descPre);

  // Resolve depends_on UUIDs to titles
  const info = document.createElement("div");
  info.className = "card-meta";
  const depLabels = task.depends_on && task.depends_on.length
    ? `depends on ${task.depends_on.map(resolveTaskTitle).join(", ")}`
    : "no dependencies";
  info.textContent = [
    depLabels,
    task.review_by ? `review: ${task.review_by}` : "no review",
    task.revision_count ? `revision ${task.revision_count}` : "",
  ].filter(Boolean).join(" \u2022 ");

  card.appendChild(info);

  if (task.result) {
    const result = document.createElement("div");
    result.className = "card-body";
    const resultPre = document.createElement("pre");
    resultPre.textContent = task.result;
    result.appendChild(resultPre);
    card.appendChild(result);
    makeCollapsible(result, resultPre);
  }

  if (task.error_output) {
    const error = document.createElement("div");
    error.className = "card-body";
    const label = document.createElement("div");
    label.className = "card-meta";
    label.textContent = "error output";
    const errorPre = document.createElement("pre");
    errorPre.textContent = task.error_output;
    error.append(label, errorPre);
    card.appendChild(error);
    makeCollapsible(error, errorPre);
  }

  if (task.review_result) {
    const review = document.createElement("div");
    review.className = "card-body";
    const reviewPre = document.createElement("pre");
    reviewPre.textContent = `review: ${task.review_result}`;
    review.appendChild(reviewPre);
    card.appendChild(review);
  }

  const actions = document.createElement("div");
  actions.className = "card-actions";

  if (task.status === "review") {
    actions.appendChild(actionButton("Accept", () => apiPost(`/api/tasks/${task.id}/approve`)));
    actions.appendChild(actionButton("Reject", () => {
      showRejectModal(task.id);
    }));
  }

  if (task.status === "failed" || task.status === "cancelled") {
    actions.appendChild(actionButton("Retry", () => apiPost(`/api/tasks/${task.id}/retry`)));
  }

  if (task.status !== "completed" && task.status !== "failed" && task.status !== "cancelled") {
    actions.appendChild(actionButton("Cancel", () => apiPost(`/api/tasks/${task.id}/cancel`)));
    const assign = document.createElement("select");
    assign.className = "select-inline btn-sm";
    appendOption(assign, "", "Reassign");
    Object.keys(state.agents).sort().forEach((name) => appendOption(assign, name, name));
    assign.addEventListener("focus", () => beginTaskInteraction(task.id));
    assign.addEventListener("mousedown", () => beginTaskInteraction(task.id));
    assign.addEventListener("blur", () => {
      window.setTimeout(() => endTaskInteraction(task.id), 0);
    });
    assign.addEventListener("change", () => {
      if (assign.value) {
        apiPost(`/api/tasks/${task.id}/reassign`, { new_agent: assign.value, reason: "manual override" });
      }
    });
    actions.appendChild(assign);
  }

  if (actions.children.length) card.appendChild(actions);
  return card;
}

function renderMessages() {
  refs.messageList.textContent = "";
  const filtered = state.messages
    .filter((message) => {
      if (!state.messageTypeFilter.has(message.type)) return false;
      if (state.agentFilter && message.from !== state.agentFilter && message.to !== state.agentFilter) return false;
      if (state.search && !JSON.stringify(message).toLowerCase().includes(state.search)) return false;
      return true;
    })
    .slice(-500);

  if (!filtered.length) {
    const empty = document.createElement("p");
    empty.className = "empty-state";
    empty.textContent = "No messages.";
    refs.messageList.appendChild(empty);
    return;
  }

  filtered.forEach((message) => {
    const card = document.createElement("article");
    const isSystem = message.type === "system";
    card.className = `message-card${isSystem ? " system-message" : ""}`;

    const header = document.createElement("div");
    header.className = "message-header";
    const from = document.createElement("span");
    from.className = "message-from";
    from.textContent = `${message.from} \u2192 ${message.to}`;
    const headerRight = document.createElement("div");
    headerRight.className = "tags";
    headerRight.append(tag(formatTime(message.timestamp)), tag(message.type));
    header.append(from, headerRight);

    const body = document.createElement("div");
    body.className = "message-body";
    const bodyPre = document.createElement("pre");
    bodyPre.textContent = message.content;
    body.appendChild(bodyPre);

    card.append(header, body);

    const metaParts = [];
    if (message.task_id) metaParts.push(`task ${message.task_id}`);
    if (message.metadata && message.metadata.tokens_in) metaParts.push(`in ${message.metadata.tokens_in}`);
    if (message.metadata && message.metadata.tokens_out) metaParts.push(`out ${message.metadata.tokens_out}`);
    if (message.metadata && message.metadata.duration_ms) metaParts.push(`${message.metadata.duration_ms}ms`);
    if (message.metadata && message.metadata.error) metaParts.push(`error: ${message.metadata.error}`);
    if (message.metadata && message.metadata.raw_output) metaParts.push("has raw output");
    if (metaParts.length) {
      const meta = document.createElement("div");
      meta.className = "card-meta";
      meta.textContent = metaParts.join(" \u2022 ");
      card.appendChild(meta);
    }

    refs.messageList.appendChild(card);
  });

  refs.messageList.scrollTop = refs.messageList.scrollHeight;
}

function phaseStatus(phase) {
  const tasks = phase.tasks || [];
  if (!tasks.length) return "empty";
  let completed = 0;
  let running = 0;
  tasks.forEach((plannedTask) => {
    const actual = state.tasks.find((task) => task.id === plannedTask.real_task_id);
    if (!actual) return;
    if (actual.status === "completed") completed += 1;
    if (actual.status === "running") running += 1;
  });
  if (completed === tasks.length) return "completed";
  if (running > 0) return "running";
  if (completed > 0) return "in progress";
  return "planned";
}

function phaseStatusTagClass(phase) {
  const status = phaseStatus(phase);
  if (status === "completed") return "tag-success";
  if (status === "running") return "tag-accent";
  if (status === "in progress") return "tag-warning";
  return "";
}

function statusTag(status) {
  const el = tag(status);
  if (status === "running" || status === "busy") el.classList.add("tag-accent");
  else if (status === "completed" || status === "idle") el.classList.add("tag-success");
  else if (status === "failed" || status === "error" || status === "blocked") el.classList.add("tag-danger");
  else if (status === "review" || status === "gated") el.classList.add("tag-warning");
  else if (status === "paused") el.classList.add("tag-purple");
  return el;
}

function toggleComposerFields() {
  const isTask = refs.sendMode.value === "task";
  const isMessage = refs.sendMode.value === "message";
  refs.targetAgent.style.display = isMessage || isTask ? "block" : "none";
  refs.taskTitle.style.display = isTask ? "block" : "none";
  refs.reviewAgent.style.display = isTask ? "block" : "none";
  refs.dependsOn.style.display = isTask ? "block" : "none";
}

async function submitGoal(event) {
  event.preventDefault();
  const parsedRounds = Number.parseInt(refs.goalInputReviewRounds.value, 10);
  const response = await apiPost("/api/goals", {
    title: refs.goalInputTitle.value,
    description: refs.goalInputDescription.value,
    workflow_recipe: refs.goalInputRecipe.value,
    max_review_rounds: Number.isFinite(parsedRounds) && parsedRounds > 0 ? parsedRounds : 0,
  });
  if (response !== false) {
    showToast("Goal submitted", "success");
    refs.goalInputTitle.value = "";
    refs.goalInputDescription.value = "";
    refs.goalInputReviewRounds.value = "";
  }
}

async function startGoal() {
  if (!state.currentGoal) return;
  const response = await apiPost(`/api/goals/${state.currentGoal.id}/start`);
  if (response !== false) showToast("Goal started", "success");
}

async function stopGoal() {
  if (!state.currentGoal) return;
  if (!window.confirm(`Stop goal "${state.currentGoal.title}"?`)) return;
  const response = await apiPost(`/api/goals/${state.currentGoal.id}/stop`);
  if (response !== false) showToast("Goal stopped", "info");
}

async function resumeGoal() {
  if (!state.currentGoal) return;
  const response = await apiPost(`/api/goals/${state.currentGoal.id}/resume`);
  if (response !== false) showToast("Goal resumed", "success");
}

async function submitComposer(event) {
  event.preventDefault();
  let response;
  if (refs.sendMode.value === "task") {
    response = await apiPost("/api/tasks", {
      title: refs.taskTitle.value,
      description: refs.composerInput.value,
      assigned_to: refs.targetAgent.value,
      review_by: refs.reviewAgent.value,
      depends_on: refs.dependsOn.value.split(",").map((value) => value.trim()).filter(Boolean),
    });
  } else {
    response = await apiPost("/api/messages", {
      to: refs.targetAgent.value,
      content: refs.composerInput.value,
    });
  }
  if (response !== false) {
    showToast(refs.sendMode.value === "task" ? "Task created" : "Message sent", "success");
    refs.composerInput.value = "";
    refs.taskTitle.value = "";
    refs.dependsOn.value = "";
  }
}

async function deleteGoal() {
  if (!state.currentGoal) return;
  if (!window.confirm(`Delete goal "${state.currentGoal.title}" permanently?`)) return;
  const response = await apiPost(`/api/goals/${state.currentGoal.id}/delete`);
  if (response !== false) showToast("Goal deleted", "info");
}

function togglePlanEditor() {
  refs.planEditor.classList.toggle("hidden");
}

async function submitPlanOverride(event) {
  event.preventDefault();
  let parsed;
  try {
    parsed = JSON.parse(refs.planInput.value);
  } catch (error) {
    showToast(`Invalid JSON: ${error.message}`, "error");
    return;
  }
  await apiPost("/api/plan", {
    plan: parsed,
    reason: "manual dashboard override",
  });
}

async function apiPost(path, body) {
  const options = { method: "POST", headers: {} };
  if (body !== undefined) {
    options.headers["Content-Type"] = "application/json";
    options.body = JSON.stringify(body);
  }
  const response = await fetch(path, options);
  if (!response.ok) {
    const payload = await response.json().catch(() => ({}));
    showToast(payload.error || `Request failed: ${response.status}`, "error");
    return false;
  }
  return true;
}

async function apiUpload(path, formData) {
  const response = await fetch(path, { method: "POST", body: formData });
  if (!response.ok) {
    const payload = await response.json().catch(() => ({}));
    showToast(payload.error || `Request failed: ${response.status}`, "error");
    return null;
  }
  return response.json().catch(() => ({}));
}

async function refreshWorkspace() {
  try {
    const response = await fetch("/api/workspace/files");
    if (!response.ok) return;
    state.workspaceFiles = await response.json();
    renderWorkspace();
    renderDiffVersionOptions();
  } catch (_) {}
}

async function uploadWorkspaceFiles() {
  const files = Array.from(refs.workspaceUploadInput.files || []);
  if (!files.length) {
    showToast("Select at least one file to upload.", "error");
    return;
  }
  const formData = new FormData();
  files.forEach((file) => formData.append("files", file));
  const targetDir = refs.workspaceUploadDir.value.trim();
  if (targetDir) formData.append("target_dir", targetDir);
  const payload = await apiUpload("/api/workspace/files", formData);
  if (!payload) return;
  showToast(`Uploaded ${files.length} file${files.length > 1 ? "s" : ""}`, "success");
  refs.workspaceUploadInput.value = "";
  await refreshWorkspace();
}

function upsertTask(task) {
  const index = state.tasks.findIndex((entry) => entry.id === task.id);
  if (index >= 0) {
    state.tasks[index] = task;
  } else {
    state.tasks.push(task);
  }
}

function upsertGoal(goal) {
  const index = state.goals.findIndex((entry) => entry.id === goal.id);
  if (index >= 0) {
    state.goals[index] = goal;
  } else {
    state.goals.push(goal);
  }
  state.currentGoal = chooseCurrentGoal() || goal;
}

function chooseCurrentGoal() {
  if (state.plan && state.plan.goal_id) {
    const goalFromPlan = state.goals.find((entry) => entry.id === state.plan.goal_id);
    if (goalFromPlan && ["planning", "active", "blocked", "stopped", "failed", "gated"].includes(goalFromPlan.status) && !isPlanCompleteForGoal(goalFromPlan.id)) {
      return goalFromPlan;
    }
  }
  return state.goals
    .slice()
    .reverse()
    .find((entry) => ["planning", "active", "blocked", "stopped", "failed", "gated"].includes(entry.status)) || null;
}

function currentTaskForAgent(agentName) {
  return state.tasks.find((task) => task.assigned_to === agentName && task.status === "running");
}

function buildFileTree(paths) {
  const root = [];
  paths.slice().sort().forEach((path) => {
    const parts = path.split("/").filter(Boolean);
    let cursor = root;
    let prefix = "";
    parts.forEach((part, index) => {
      prefix = prefix ? `${prefix}/${part}` : part;
      let entry = cursor.find((node) => node.name === part);
      if (!entry) {
        entry = {
          name: part,
          path: prefix,
          type: index === parts.length - 1 ? "file" : "folder",
          children: [],
        };
        cursor.push(entry);
      }
      if (index < parts.length - 1) entry.type = "folder";
      cursor = entry.children;
    });
  });
  return root;
}

function renderTreeNode(node, depth) {
  if (depth === undefined) depth = 0;
  const wrapper = document.createElement("div");
  wrapper.className = "tree-node";

  const entry = document.createElement("div");
  entry.className = `tree-entry ${node.type}`;
  entry.title = node.path;

  if (node.type === "folder") {
    const chevron = document.createElement("span");
    chevron.className = "tree-chevron";
    chevron.textContent = "▶";
    entry.appendChild(chevron);

    const label = document.createElement("span");
    label.textContent = node.name;
    entry.appendChild(label);

    const count = document.createElement("span");
    count.className = "tree-count";
    count.textContent = node.children ? node.children.length : 0;
    entry.appendChild(count);
  } else {
    entry.textContent = node.name;
  }

  if (node.type === "file") {
    entry.addEventListener("click", () => openFileViewer(node.path));
  }

  wrapper.appendChild(entry);

  if (node.type === "folder" && node.children && node.children.length) {
    const children = document.createElement("div");
    children.className = "tree-children";
    // Start collapsed for folders deeper than level 1
    const startExpanded = depth < 1;
    if (!startExpanded) {
      children.classList.add("collapsed");
      entry.querySelector(".tree-chevron").textContent = "▶";
    } else {
      entry.querySelector(".tree-chevron").textContent = "▼";
    }
    node.children.forEach((child) => {
      children.appendChild(renderTreeNode(child, depth + 1));
    });
    wrapper.appendChild(children);

    entry.addEventListener("click", () => {
      const isCollapsed = children.classList.toggle("collapsed");
      entry.querySelector(".tree-chevron").textContent = isCollapsed ? "▶" : "▼";
    });
    entry.style.cursor = "pointer";
  }

  return wrapper;
}

function actionButton(label, handler) {
  const button = document.createElement("button");
  button.type = "button";
  button.className = "btn-sm";
  button.textContent = label;
  button.addEventListener("click", handler);
  return button;
}

function tag(text) {
  const span = document.createElement("span");
  span.className = "tag";
  span.textContent = text;
  return span;
}

function elapsed(timestamp) {
  if (!timestamp) return "0s";
  const totalSeconds = Math.max(0, Math.floor((Date.now() - Date.parse(timestamp)) / 1000));
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return minutes > 0 ? `${minutes}m ${seconds}s` : `${seconds}s`;
}

function formatTime(timestamp) {
  return new Date(timestamp).toLocaleTimeString();
}

window.addEventListener("DOMContentLoaded", init);
