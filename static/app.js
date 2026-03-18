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
};

const messageTypes = [
  "human→coordinator",
  "coordinator→agent",
  "agent→coordinator",
  "agent→agent",
  "coordinator→human",
  "system",
];

let socket;
let reconnectDelay = 1000;

function initTabs() {
  const tabs = document.querySelectorAll(".tab[data-tab]");
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

function init() {
  initTabs();

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

  toggleComposerFields();
  renderHumanInputRequest();
  connectWebSocket();
  window.setInterval(() => {
    renderAgents();
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
  refreshWorkspace();
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
    return;
  }

  refs.goalbar.classList.remove("empty");
  const goalTasks = state.tasks.filter((task) => task.goal_id === goal.id);
  const completed = goalTasks.filter((task) => task.status === "completed").length;
  const total = goalTasks.length;
  const percent = total === 0 ? 0 : Math.round((completed / total) * 100);

  refs.goalTitle.textContent = goal.title;

  const parts = [goal.status];
  if (state.workflow.stage) parts.push(state.workflow.stage.replaceAll("_", " "));
  if (goal.workflow_recipe) parts.push(formatRecipeLabel(goal.workflow_recipe));
  if (state.workflow.recipe === "spec-review-loop" || state.workflow.recipe === "spec-cross-critique-loop") {
    if (state.workflow.review_round || goal.max_review_rounds) {
      const activeRound = state.workflow.review_round || 0;
      const maxRounds = goal.max_review_rounds || state.workflow.max_review_rounds || 0;
      parts.push(maxRounds ? `review round ${activeRound}/${maxRounds}` : `review round ${activeRound}`);
    }
    if (state.workflow.stage_task_total) {
      let progressLabel = "tasks";
      if (state.workflow.stage === "adversarial_review") progressLabel = "reviewers";
      if (state.workflow.stage === "cross_critique") progressLabel = "critiques";
      parts.push(`${progressLabel} ${state.workflow.stage_task_completed || 0}/${state.workflow.stage_task_total} complete`);
    }
  } else if (state.workflow.current_phase_number) {
    const phaseLabel = state.workflow.total_phases
      ? `phase ${state.workflow.current_phase_number}/${state.workflow.total_phases}`
      : `phase ${state.workflow.current_phase_number}`;
    parts.push(state.workflow.current_phase_title ? `${phaseLabel} ${state.workflow.current_phase_title}` : phaseLabel);
  }
  if (goal.summary && (goal.status === "blocked" || goal.status === "failed" || goal.status === "stopped")) {
    parts.push(clampText(goal.summary, 220));
  } else if (goal.description) {
    parts.push(clampText(goal.description, 180));
  }

  refs.goalStatus.textContent = parts.filter(Boolean).join(" • ");
  refs.goalProgressLabel.textContent = `${completed} / ${total}`;
  refs.goalProgressBar.style.width = `${percent}%`;
  refs.startGoal.classList.toggle("hidden", !canStartGoal(goal));
  refs.stopGoal.classList.toggle("hidden", !canStopGoal(goal));
  refs.resumeGoal.classList.toggle("hidden", !canResumeGoal(goal));
  refs.deleteGoal.classList.toggle("hidden", !canDeleteGoal(goal));
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
  if (!["blocked", "failed"].includes(goal.status)) return false;
  return !isPlanCompleteForGoal(goal.id);
}

function canDeleteGoal(goal) {
  return Boolean(goal);
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
    header.textContent = `${formatTime(message.timestamp)} • ${message.content}`;
    item.appendChild(header);

    const meta = [];
    if (message.metadata && message.metadata.goal) meta.push(`goal ${message.metadata.goal.status}`);
    if (message.metadata && message.metadata.task) meta.push(`task ${message.metadata.task.status}`);
    if (message.metadata && message.metadata.plan) meta.push("plan update");
    if (meta.length) {
      const metaEl = document.createElement("div");
      metaEl.className = "card-meta";
      metaEl.textContent = meta.join(" • ");
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
  return parts.join(" • ");
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
  return parts.join(" • ");
}

function formatWorkflowProcess(invocation, workflow) {
  if (!invocation) {
    if (workflow && workflow.status === "stopped" && workflow.last_error) {
      return `Workflow stopped • ${workflow.last_error}`;
    }
    if (workflow && workflow.status === "blocked" && workflow.last_error) {
      return `Workflow blocked • ${workflow.last_error}`;
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
  return parts.join(" • ");
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
      taskTag.textContent = actual ? `${actual.assigned_to} • ${actual.status}` : `${plannedTask.assign_to} • planned`;
      taskHeader.append(taskTitle, taskTag);

      const meta = document.createElement("div");
      meta.className = "tags";
      meta.style.marginTop = "4px";
      meta.append(
        tag(`p${plannedTask.priority || 3}`),
        tag(plannedTask.review_by ? `review: ${plannedTask.review_by}` : "no review"),
      );

      const desc = document.createElement("pre");
      desc.textContent = plannedTask.description;

      const deps = document.createElement("div");
      deps.className = "card-meta";
      deps.textContent = plannedTask.depends_on && plannedTask.depends_on.length
        ? `depends on ${resolvePlanDependencyLabels(plannedTask.depends_on, plannedTaskLookup).join(", ")}`
        : "no dependencies";

      const refs = document.createElement("div");
      refs.className = "card-meta";
      const referenceFiles = actual
        ? combineReferenceList(actual.discussion_file, actual.files_touched, actual.files_changed)
        : [];
      refs.textContent = referenceFiles.length ? `files ${referenceFiles.join(", ")}` : "no file refs yet";

      taskEl.append(taskHeader, meta, desc, deps, refs);
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
  return `${value.slice(0, limit - 1)}…`;
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

    const detail = document.createElement("div");
    detail.className = "card-meta";
    detail.textContent = activeTask
      ? `task: ${activeTask.title} • ${elapsed(activeTask.started_at || activeTask.created_at)}`
      : "no active task";

    const process = document.createElement("div");
    process.className = "card-meta";
    process.textContent = formatAgentProcess(agent);

    const stats = document.createElement("div");
    stats.className = "card-meta";
    stats.textContent = `completed ${agent.tasks_completed || 0} • failed ${agent.tasks_failed || 0} • tokens ${agent.total_tokens_in || 0}/${agent.total_tokens_out || 0}`;

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
  return parts.join(" • ");
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
  refs.tabTasksCount.textContent = activeCount > 0 ? `(${activeCount})` : "";

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
  const title = document.createElement("span");
  title.className = "card-title";
  title.textContent = task.title;
  const tags = document.createElement("div");
  tags.className = "tags";
  tags.style.marginTop = "4px";
  tags.append(
    tag(task.assigned_to),
    statusTag(task.status),
    tag(`p${task.priority || 3}`),
    task.goal_id ? tag(`goal ${task.goal_id.slice(0, 8)}`) : tag("manual"),
  );
  titleWrap.append(title, tags);
  const timer = document.createElement("span");
  timer.className = "card-meta";
  timer.style.marginTop = "0";
  timer.dataset.taskTimestamp = task.started_at || task.created_at || "";
  timer.dataset.taskRunning = task.status === "running" ? "true" : "false";
  timer.textContent = task.status === "running" ? elapsed(task.started_at || task.created_at) : formatTime(task.created_at);
  header.append(titleWrap, timer);

  const desc = document.createElement("div");
  desc.className = "card-body";
  const descPre = document.createElement("pre");
  descPre.textContent = task.description;
  desc.appendChild(descPre);

  const info = document.createElement("div");
  info.className = "card-meta";
  info.textContent = [
    task.depends_on && task.depends_on.length ? `depends on ${task.depends_on.join(", ")}` : "no dependencies",
    task.review_by ? `review: ${task.review_by}` : "no review",
    task.revision_count ? `revision ${task.revision_count}` : "",
  ].filter(Boolean).join(" • ");

  card.append(header, desc, info);

  if (task.result) {
    const result = document.createElement("div");
    result.className = "card-body";
    const resultPre = document.createElement("pre");
    resultPre.textContent = task.result;
    result.appendChild(resultPre);
    card.appendChild(result);
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
      const reason = window.prompt("Reason for rejection");
      if (reason) apiPost(`/api/tasks/${task.id}/reject`, { reason });
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
    from.textContent = `${message.from} → ${message.to}`;
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
      meta.textContent = metaParts.join(" • ");
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
  else if (status === "review") el.classList.add("tag-warning");
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
  await apiPost("/api/goals", {
    title: refs.goalInputTitle.value,
    description: refs.goalInputDescription.value,
    workflow_recipe: refs.goalInputRecipe.value,
    max_review_rounds: Number.isFinite(parsedRounds) && parsedRounds > 0 ? parsedRounds : 0,
  });
  refs.goalInputTitle.value = "";
  refs.goalInputDescription.value = "";
  refs.goalInputReviewRounds.value = "";
}

async function startGoal() {
  if (!state.currentGoal) return;
  await apiPost(`/api/goals/${state.currentGoal.id}/start`);
}

async function stopGoal() {
  if (!state.currentGoal) return;
  if (!window.confirm(`Stop goal "${state.currentGoal.title}"?`)) return;
  await apiPost(`/api/goals/${state.currentGoal.id}/stop`);
}

async function resumeGoal() {
  if (!state.currentGoal) return;
  await apiPost(`/api/goals/${state.currentGoal.id}/resume`);
}

async function submitComposer(event) {
  event.preventDefault();
  if (refs.sendMode.value === "task") {
    await apiPost("/api/tasks", {
      title: refs.taskTitle.value,
      description: refs.composerInput.value,
      assigned_to: refs.targetAgent.value,
      review_by: refs.reviewAgent.value,
      depends_on: refs.dependsOn.value.split(",").map((value) => value.trim()).filter(Boolean),
    });
  } else {
    await apiPost("/api/messages", {
      to: refs.targetAgent.value,
      content: refs.composerInput.value,
    });
  }
  refs.composerInput.value = "";
  refs.taskTitle.value = "";
  refs.dependsOn.value = "";
}

async function deleteGoal() {
  if (!state.currentGoal) return;
  if (!window.confirm(`Delete goal "${state.currentGoal.title}" permanently?`)) return;
  await apiPost(`/api/goals/${state.currentGoal.id}/delete`);
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
    window.alert(`Invalid JSON: ${error.message}`);
    return;
  }
  await apiPost("/api/plan", {
    plan: parsed,
    reason: "manual dashboard override",
  });
}

async function apiPost(path, body = undefined) {
  const options = { method: "POST", headers: {} };
  if (body !== undefined) {
    options.headers["Content-Type"] = "application/json";
    options.body = JSON.stringify(body);
  }
  const response = await fetch(path, options);
  if (!response.ok) {
    const payload = await response.json().catch(() => ({}));
    window.alert(payload.error || `Request failed: ${response.status}`);
  }
}

async function apiUpload(path, formData) {
  const response = await fetch(path, { method: "POST", body: formData });
  if (!response.ok) {
    const payload = await response.json().catch(() => ({}));
    window.alert(payload.error || `Request failed: ${response.status}`);
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
  } catch (_) {}
}

async function uploadWorkspaceFiles() {
  const files = Array.from(refs.workspaceUploadInput.files || []);
  if (!files.length) {
    window.alert("Select at least one file to upload.");
    return;
  }
  const formData = new FormData();
  files.forEach((file) => formData.append("files", file));
  const targetDir = refs.workspaceUploadDir.value.trim();
  if (targetDir) formData.append("target_dir", targetDir);
  const payload = await apiUpload("/api/workspace/files", formData);
  if (!payload) return;
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
    if (goalFromPlan && ["planning", "active", "blocked", "stopped", "failed"].includes(goalFromPlan.status) && !isPlanCompleteForGoal(goalFromPlan.id)) {
      return goalFromPlan;
    }
  }
  return state.goals
    .slice()
    .reverse()
    .find((entry) => ["planning", "active", "blocked", "stopped", "failed"].includes(entry.status)) || null;
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

function renderTreeNode(node) {
  const wrapper = document.createElement("div");
  wrapper.className = "tree-node";

  const entry = document.createElement("div");
  entry.className = `tree-entry ${node.type}`;
  entry.textContent = node.type === "folder" ? `/${node.name}` : node.name;
  entry.title = node.path;
  wrapper.appendChild(entry);

  if (node.children && node.children.length) {
    const children = document.createElement("div");
    children.className = "tree-children";
    node.children.forEach((child) => {
      children.appendChild(renderTreeNode(child));
    });
    wrapper.appendChild(children);
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
