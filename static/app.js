const state = {
  agents: {},
  tasks: [],
  goals: [],
  currentGoal: null,
  plan: null,
  brain: {},
  workflow: {},
  workspace: {},
  workspaceFiles: [],
  messages: [],
  search: "",
  agentFilter: "",
  messageTypeFilter: new Set(),
  lastBrainDecisions: [],
  humanInputRequest: null,
};

const refs = {
  connection: document.getElementById("connection-status"),
  brainProviderLabel: document.getElementById("brain-provider-label"),
  workspacePath: document.getElementById("workspace-path"),
  refreshWorkspace: document.getElementById("refresh-workspace"),
  workspaceTree: document.getElementById("workspace-tree"),
  brainProviderSelect: document.getElementById("brain-provider-select"),
  switchBrain: document.getElementById("switch-brain"),
  forceReplan: document.getElementById("force-replan"),
  killGoal: document.getElementById("kill-goal"),
  goalTitle: document.getElementById("goal-title"),
  goalStatus: document.getElementById("goal-status"),
  goalSummary: document.getElementById("goal-summary"),
  goalProgressLabel: document.getElementById("goal-progress-label"),
  goalProgressBar: document.getElementById("goal-progress-bar"),
  goalForm: document.getElementById("goal-form"),
  goalInputTitle: document.getElementById("goal-input-title"),
  goalInputDescription: document.getElementById("goal-input-description"),
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
};

const messageTypes = [
  "human→coordinator",
  "coordinator→agent",
  "agent→coordinator",
  "agent→agent",
  "coordinator→human",
  "system",
];

const brainProviders = ["claude", "codex"];

let socket;
let reconnectDelay = 1000;

function init() {
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

  brainProviders.forEach((provider) => {
    appendOption(refs.brainProviderSelect, provider, provider);
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
  refs.forceReplan.addEventListener("click", forceReplan);
  refs.switchBrain.addEventListener("click", switchBrain);
  refs.killGoal.addEventListener("click", killGoal);
  refs.refreshWorkspace.addEventListener("click", refreshWorkspace);
  refs.goalForm.addEventListener("submit", submitGoal);
  refs.composerForm.addEventListener("submit", submitComposer);
  refs.sendMode.addEventListener("change", toggleComposerFields);
  refs.togglePlanEditor.addEventListener("click", togglePlanEditor);
  refs.planForm.addEventListener("submit", submitPlanOverride);

  toggleComposerFields();
  connectWebSocket();
  window.setInterval(() => {
    renderAgents();
    renderTasks();
  }, 1000);
}

function connectWebSocket() {
  const protocol = window.location.protocol === "https:" ? "wss" : "ws";
  socket = new WebSocket(`${protocol}://${window.location.host}/ws`);

  socket.addEventListener("open", () => {
    refs.connection.textContent = "connected";
    refs.connection.className = "connection status-idle";
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
        break;
      case "task_update":
        upsertTask(payload.data);
        renderTasks();
        renderPlan();
        refreshWorkspace();
        break;
      case "agent_status":
        state.agents[payload.data.name] = payload.data;
        renderAgents();
        populateAgentOptions();
        break;
      case "goal_update":
        upsertGoal(payload.data);
        if (state.currentGoal && state.currentGoal.id === payload.data.id) {
          state.currentGoal = payload.data;
        } else if (!state.currentGoal && ["planning", "active"].includes(payload.data.status)) {
          state.currentGoal = payload.data;
        }
        renderGoalBar();
        break;
      case "plan_update":
        state.plan = payload.data;
        renderGoalBar();
        renderPlan();
        break;
      case "brain_thinking":
        state.brain = {
          ...state.brain,
          last_thinking: payload.data.thinking,
          last_trigger: payload.data.trigger,
          invocation_in_flight: payload.data.invocation_in_flight ?? state.brain.invocation_in_flight,
        };
        renderBrainPanel();
        break;
      case "brain_status":
        state.brain = {
          ...state.brain,
          invocation_in_flight: !["succeeded", "failed", "timed_out", "cancelled", "start_failed"].includes(payload.data.status),
          last_invocation: payload.data,
        };
        renderBrainPanel();
        break;
      case "brain_decisions":
        state.lastBrainDecisions = payload.data.decisions || [];
        renderBrainPanel();
        break;
      case "workflow_update":
        state.workflow = payload.data || {};
        renderGoalBar();
        renderBrainPanel();
        break;
      case "human_input_requested":
        state.humanInputRequest = payload.data;
        renderHumanInputRequest();
        break;
    }
  });

  socket.addEventListener("close", () => {
    refs.connection.textContent = "reconnecting";
    refs.connection.className = "connection status-busy";
    window.setTimeout(connectWebSocket, reconnectDelay);
    reconnectDelay = Math.min(reconnectDelay * 2, 30000);
  });
}

function hydrateState(data) {
  state.agents = data.agents || {};
  state.tasks = data.tasks || [];
  state.goals = data.goals || [];
  state.currentGoal = data.current_goal || null;
  state.plan = data.plan || null;
  state.brain = data.brain || {};
  state.workflow = data.workflow || {};
  state.workspace = data.workspace || {};
  state.messages = data.messages || [];
  state.humanInputRequest = state.brain.pending_human_input || null;
}

function renderAll() {
  populateAgentOptions();
  toggleComposerFields();
  renderGoalBar();
  renderHumanInputRequest();
  renderBrainPanel();
  renderPlan();
  renderAgents();
  renderTasks();
  renderMessages();
  renderWorkspace();
  refreshWorkspace();
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
    } else if (index === 2) {
      appendOption(select, "", "No reviewer");
    }
    names.forEach((name) => appendOption(select, name, name));
    if (existing && [...select.options].some((option) => option.value === existing)) {
      select.value = existing;
    }
  });

  const provider = state.brain.active_provider || state.brainProvider || state.brain.provider || "claude";
  refs.brainProviderLabel.textContent = provider;
  refs.brainProviderSelect.value = provider;
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
    refs.goalTitle.textContent = "No active goal";
    refs.goalStatus.textContent = "Submit a goal to start planning.";
    refs.goalSummary.textContent = "";
    refs.goalProgressLabel.textContent = "0 / 0 tasks";
    refs.goalProgressBar.style.width = "0%";
    return;
  }

  const goalTasks = state.tasks.filter((task) => task.goal_id === goal.id);
  const completed = goalTasks.filter((task) => task.status === "completed").length;
  const total = goalTasks.length;
  const percent = total === 0 ? 0 : Math.round((completed / total) * 100);

  refs.goalTitle.textContent = goal.title;
  refs.goalStatus.textContent = `${goal.status} • ${goal.description || "No description"}`;
  if (state.workflow && state.workflow.status && state.workflow.status !== goal.status) {
    refs.goalStatus.textContent = `${refs.goalStatus.textContent} • ${state.workflow.status}`;
  }
  refs.goalSummary.textContent = goal.summary || "";
  refs.goalProgressLabel.textContent = `${completed} / ${total} tasks`;
  refs.goalProgressBar.style.width = `${percent}%`;
}

function renderWorkspace() {
  refs.workspacePath.textContent = `Workspace: ${state.workspace.path || "unknown"}`;
  refs.workspaceTree.textContent = "";
  if (!state.workspaceFiles.length) {
    const empty = document.createElement("p");
    empty.className = "muted";
    empty.textContent = "No workspace files found.";
    refs.workspaceTree.appendChild(empty);
    return;
  }

  buildFileTree(state.workspaceFiles).forEach((node) => {
    refs.workspaceTree.appendChild(renderTreeNode(node));
  });
}

function renderHumanInputRequest() {
  if (!state.humanInputRequest) {
    refs.humanInputPanel.classList.add("hidden");
    refs.humanInputPanel.textContent = "";
    return;
  }

  refs.humanInputPanel.classList.remove("hidden");
  refs.humanInputPanel.textContent = "";

  const title = document.createElement("strong");
  title.textContent = state.humanInputRequest.question;
  const context = document.createElement("p");
  context.className = "muted";
  context.textContent = state.humanInputRequest.context || "";
  const form = document.createElement("form");
  form.className = "form-grid";
  const input = document.createElement("textarea");
  input.rows = 3;
  input.placeholder = "Respond to the coordinator brain";
  const button = document.createElement("button");
  button.type = "submit";
  button.textContent = "Send Response";
  form.append(input, button);
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    await apiPost("/api/brain/respond", { answer: input.value });
    input.value = "";
    state.humanInputRequest = null;
    renderHumanInputRequest();
  });

  refs.humanInputPanel.append(title, context, form);
}

function renderBrainPanel() {
  const trigger = state.brain.last_trigger || "no trigger";
  refs.brainTrigger.textContent = trigger;
  const inFlight = Boolean(state.brain.invocation_in_flight);
  refs.brainInvocations.textContent = `${state.brain.invocation_count || 0} invocations • tokens ${state.brain.total_tokens_in || 0}/${state.brain.total_tokens_out || 0}${inFlight ? " • running" : ""}`;
  refs.workflowStatus.textContent = formatWorkflowStatus(state.workflow);
  refs.brainProcess.textContent = formatBrainProcess(state.brain.last_invocation, state.workflow);
  refs.brainThinking.textContent = state.brain.last_thinking || "No brain output yet.";
  refs.brainProviderLabel.textContent = state.brain.active_provider || refs.brainProviderLabel.textContent;

  refs.brainDecisions.textContent = "";
  if (!state.lastBrainDecisions.length) {
    const empty = document.createElement("p");
    empty.className = "muted";
    empty.textContent = "No decisions recorded yet.";
    refs.brainDecisions.appendChild(empty);
    return;
  }

  state.lastBrainDecisions.forEach((decision) => {
    const item = document.createElement("article");
    item.className = "decision-item";
    const action = document.createElement("strong");
    action.textContent = decision.action;
    const content = document.createElement("pre");
    content.textContent = JSON.stringify(decision, null, 2);
    item.append(action, content);
    refs.brainDecisions.appendChild(item);
  });
}

function formatBrainProcess(invocation, workflow) {
  if (!invocation) {
    if (workflow && workflow.mode) {
      return workflow.planner_mode === "disabled" ? "Planner idle. Deterministic workflow is driving execution." : "Planner idle. Deterministic workflow is driving execution unless a manual re-plan is requested.";
    }
    return "No brain process telemetry yet.";
  }
  const parts = [
    invocation.status || "unknown",
    invocation.command || "unknown command",
  ];
  if (invocation.pid) {
    parts.push(`pid ${invocation.pid}`);
  }
  if (invocation.timeout_seconds) {
    parts.push(`timeout ${invocation.timeout_seconds}s`);
  }
  if (invocation.stdout_bytes || invocation.stderr_bytes) {
    parts.push(`io ${invocation.stdout_bytes || 0}/${invocation.stderr_bytes || 0} bytes`);
  }
  if (invocation.trace_id) {
    parts.push(`trace ${invocation.trace_id.slice(0, 12)}`);
  }
  if (invocation.error) {
    parts.push(invocation.error);
  }
  return parts.join(" • ");
}

function formatWorkflowStatus(workflow) {
  if (!workflow || !workflow.mode) {
    return "No active workflow state.";
  }
  const parts = [workflow.mode];
  if (workflow.planner_mode) {
    parts.push(`planner ${workflow.planner_mode}`);
  }
  if (workflow.status) {
    parts.push(workflow.status);
  }
  if (workflow.current_phase_number) {
    const phaseLabel = workflow.total_phases
      ? `phase ${workflow.current_phase_number}/${workflow.total_phases}`
      : `phase ${workflow.current_phase_number}`;
    parts.push(workflow.current_phase_title ? `${phaseLabel} ${workflow.current_phase_title}` : phaseLabel);
  }
  return parts.join(" • ");
}

function renderPlan() {
  refs.planView.textContent = "";
  refs.planInput.value = state.plan ? JSON.stringify(state.plan, null, 2) : "";
  if (!state.plan || !state.plan.phases || state.plan.phases.length === 0) {
    const empty = document.createElement("p");
    empty.className = "muted";
    empty.textContent = "No active plan.";
    refs.planView.appendChild(empty);
    return;
  }

  state.plan.phases.forEach((phase) => {
    const phaseCard = document.createElement("article");
    phaseCard.className = "phase-card";

    const head = document.createElement("div");
    head.className = "plan-head";
    const title = document.createElement("div");
    const phaseTitle = document.createElement("strong");
    phaseTitle.textContent = `Phase ${phase.number}: ${phase.title}`;
    const phaseDesc = document.createElement("p");
    phaseDesc.className = "muted";
    phaseDesc.textContent = phase.description || "";
    title.append(phaseTitle, phaseDesc);

    const status = document.createElement("span");
    status.className = "badge";
    status.textContent = phaseStatus(phase);
    head.append(title, status);

    const tasks = document.createElement("div");
    tasks.className = "phase-tasks";
    (phase.tasks || []).forEach((plannedTask) => {
      const actual = state.tasks.find((task) => task.id === plannedTask.real_task_id);
      const taskCard = document.createElement("div");
      taskCard.className = `phase-task ${actual && actual.status === "completed" ? "completed" : ""}`;

      const taskHead = document.createElement("div");
      taskHead.className = "phase-task-head";
      const taskTitle = document.createElement("strong");
      taskTitle.textContent = plannedTask.title;
      const taskBadge = document.createElement("span");
      taskBadge.className = "pill";
      taskBadge.textContent = actual ? `${actual.assigned_to} • ${actual.status}` : `${plannedTask.assign_to} • planned`;
      taskHead.append(taskTitle, taskBadge);

      const meta = document.createElement("div");
      meta.className = "phase-task-meta";
      meta.append(
        pill(`priority ${plannedTask.priority || 3}`),
        pill(plannedTask.review_by ? `review ${plannedTask.review_by}` : "no review"),
      );

      const desc = document.createElement("pre");
      desc.textContent = plannedTask.description;
      const deps = document.createElement("p");
      deps.className = "muted";
      deps.textContent = plannedTask.depends_on && plannedTask.depends_on.length
        ? `depends on ${plannedTask.depends_on.join(", ")}`
        : "no dependencies";

      taskCard.append(taskHead, meta, desc, deps);
      tasks.appendChild(taskCard);
    });

    phaseCard.append(head, tasks);
    refs.planView.appendChild(phaseCard);
  });
}

function renderAgents() {
  refs.agentsList.textContent = "";
  Object.values(state.agents)
    .sort((a, b) => a.name.localeCompare(b.name))
    .forEach((agent) => {
      const card = document.createElement("article");
      card.className = "agent-card";
      if (state.agentFilter === agent.name) {
        card.classList.add("active");
      }

      const head = document.createElement("div");
      head.className = "agent-head";
      const left = document.createElement("div");
      const name = document.createElement("strong");
      name.textContent = agent.name;
      const activeTask = currentTaskForAgent(agent.name);
      const meta = document.createElement("div");
      meta.className = "agent-meta";
      meta.append(pill(agent.provider || "unknown"), pill(agent.role || "unknown"), pill(agent.status));
      if (activeTask && activeTask.revision_count) {
        meta.appendChild(pill(`rev ${activeTask.revision_count}`));
      }
      left.append(name, meta);

      const status = document.createElement("span");
      status.innerHTML = `<span class="status-dot status-${agent.status}"></span>`;
      head.append(left, status);

      const detail = document.createElement("p");
      detail.className = "muted";
      detail.textContent = activeTask
        ? `task ${activeTask.title} • ${elapsed(activeTask.started_at || activeTask.created_at)}`
        : "no active task";

      const process = document.createElement("p");
      process.className = "muted";
      process.textContent = formatAgentProcess(agent);

      const stats = document.createElement("p");
      stats.className = "muted";
      stats.textContent = `completed ${agent.tasks_completed || 0} • failed ${agent.tasks_failed || 0} • tokens ${agent.total_tokens_in || 0}/${agent.total_tokens_out || 0}`;

      const actions = document.createElement("div");
      actions.className = "agent-actions";

      const filter = document.createElement("button");
      filter.type = "button";
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

      card.append(head, detail, process, stats, actions);
      refs.agentsList.appendChild(card);
    });
}

function formatAgentProcess(agent) {
  const invocation = agent.last_invocation;
  if (!invocation) {
    return "no worker telemetry yet";
  }
  const parts = [invocation.status || "unknown"];
  if (invocation.pid) {
    parts.push(`pid ${invocation.pid}`);
  }
  if (invocation.timeout_seconds) {
    parts.push(`timeout ${invocation.timeout_seconds}s`);
  }
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
    if (groups[status]) {
      groups[status].push(task);
    }
  });

  Object.entries(groups).forEach(([status, tasks]) => {
    if (!tasks.length) {
      return;
    }
    const group = document.createElement("section");
    group.className = "task-group";
    const head = document.createElement("div");
    head.className = "group-title";
    const title = document.createElement("strong");
    title.textContent = `${status} (${tasks.length})`;
    head.appendChild(title);
    group.appendChild(head);

    tasks
      .slice()
      .sort((a, b) => (a.priority || 3) - (b.priority || 3) || Date.parse(a.created_at) - Date.parse(b.created_at))
      .forEach((task) => {
        group.appendChild(renderTaskCard(task));
      });

    refs.tasksList.appendChild(group);
  });
}

function renderTaskCard(task) {
  const card = document.createElement("article");
  card.className = `task-card ${task.status}`;

  const head = document.createElement("div");
  head.className = "task-head";
  const title = document.createElement("div");
  const strong = document.createElement("strong");
  strong.textContent = task.title;
  const meta = document.createElement("div");
  meta.className = "task-meta";
  meta.append(
    pill(task.assigned_to),
    pill(task.status),
    pill(`p${task.priority || 3}`),
    task.goal_id ? pill(`goal ${task.goal_id.slice(0, 8)}`) : pill("manual"),
  );
  title.append(strong, meta);
  const timer = document.createElement("span");
  timer.className = "muted";
  timer.textContent = task.status === "running" ? elapsed(task.started_at || task.created_at) : formatTime(task.created_at);
  head.append(title, timer);

  const desc = document.createElement("pre");
  desc.textContent = task.description;

  const info = document.createElement("p");
  info.className = "muted";
  info.textContent = [
    task.depends_on && task.depends_on.length ? `depends on ${task.depends_on.join(", ")}` : "no dependencies",
    task.review_by ? `review ${task.review_by}` : "no review",
    task.revision_count ? `revision ${task.revision_count}` : "",
  ].filter(Boolean).join(" • ");

  card.append(head, desc, info);

  if (task.result) {
    const result = document.createElement("pre");
    result.textContent = task.result;
    card.appendChild(result);
  }

  if (task.review_result) {
    const review = document.createElement("pre");
    review.textContent = `review: ${task.review_result}`;
    card.appendChild(review);
  }

  const actions = document.createElement("div");
  actions.className = "task-actions";

  if (task.status === "review") {
    actions.appendChild(actionButton("Accept", () => apiPost(`/api/tasks/${task.id}/approve`)));
    actions.appendChild(actionButton("Reject", () => {
      const reason = window.prompt("Reason for rejection");
      if (reason) {
        apiPost(`/api/tasks/${task.id}/reject`, { reason });
      }
    }));
  }

  if (task.status !== "completed" && task.status !== "failed" && task.status !== "cancelled") {
    actions.appendChild(actionButton("Cancel", () => apiPost(`/api/tasks/${task.id}/cancel`)));
    const assign = document.createElement("select");
    assign.className = "select-inline";
    appendOption(assign, "", "Reassign");
    Object.keys(state.agents).sort().forEach((name) => appendOption(assign, name, name));
    assign.addEventListener("change", () => {
      if (assign.value) {
        apiPost(`/api/tasks/${task.id}/reassign`, { new_agent: assign.value, reason: "manual override" });
      }
    });
    actions.appendChild(assign);
  }

  if (actions.children.length) {
    card.appendChild(actions);
  }
  return card;
}

function renderMessages() {
  refs.messageList.textContent = "";
  const filtered = state.messages.filter((message) => {
    if (!state.messageTypeFilter.has(message.type)) {
      return false;
    }
    if (state.agentFilter && message.from !== state.agentFilter && message.to !== state.agentFilter) {
      return false;
    }
    if (state.search && !JSON.stringify(message).toLowerCase().includes(state.search)) {
      return false;
    }
    return true;
  }).slice(-500);

  filtered.forEach((message) => {
    const card = document.createElement("article");
    const brainMessage = message.from === "brain" || message.to === "brain";
    card.className = `message-card ${brainMessage ? "brain-message" : ""} ${message.type === "system" ? "system-message" : ""} ${brainMessage && message.metadata && !message.metadata.task ? "brain-thinking" : ""}`;

    const head = document.createElement("div");
    head.className = "message-head";
    const title = document.createElement("strong");
    title.textContent = `${formatTime(message.timestamp)} ${message.from} → ${message.to}`;
    const badge = document.createElement("span");
    badge.className = "badge";
    badge.textContent = message.type;
    head.append(title, badge);

    const body = document.createElement("pre");
    body.textContent = message.content;
    card.append(head, body);

    const metaParts = [];
    if (message.task_id) metaParts.push(`task ${message.task_id}`);
    if (message.metadata && message.metadata.tokens_in) metaParts.push(`tokens in ${message.metadata.tokens_in}`);
    if (message.metadata && message.metadata.tokens_out) metaParts.push(`tokens out ${message.metadata.tokens_out}`);
    if (message.metadata && message.metadata.duration_ms) metaParts.push(`duration ${message.metadata.duration_ms}ms`);
    if (message.metadata && message.metadata.error) metaParts.push(`error ${message.metadata.error}`);
    if (metaParts.length) {
      const meta = document.createElement("p");
      meta.className = "muted";
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
  await apiPost("/api/goals", {
    title: refs.goalInputTitle.value,
    description: refs.goalInputDescription.value,
  });
  refs.goalInputTitle.value = "";
  refs.goalInputDescription.value = "";
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
  } else if (refs.sendMode.value === "message") {
    await apiPost("/api/messages", {
      to: refs.targetAgent.value,
      content: refs.composerInput.value,
    });
  } else {
    await apiPost("/api/brain/respond", {
      answer: refs.composerInput.value,
    });
  }

  refs.composerInput.value = "";
  refs.taskTitle.value = "";
  refs.dependsOn.value = "";
}

async function forceReplan() {
  const guidance = window.prompt("Optional guidance for the re-plan");
  await apiPost("/api/brain/replan", { guidance: guidance || "" });
}

async function switchBrain() {
  await apiPost("/api/brain/switch", { provider: refs.brainProviderSelect.value });
}

async function killGoal() {
  if (!state.currentGoal) {
    return;
  }
  if (!window.confirm(`Kill goal "${state.currentGoal.title}"?`)) {
    return;
  }
  await apiPost(`/api/goals/${state.currentGoal.id}/kill`);
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

async function refreshWorkspace() {
  try {
    const response = await fetch("/api/workspace/files");
    if (!response.ok) {
      return;
    }
    state.workspaceFiles = await response.json();
    renderWorkspace();
  } catch (_) {
  }
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
  state.currentGoal = state.goals.find((entry) => ["planning", "active"].includes(entry.status)) || goal;
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
      if (index < parts.length - 1) {
        entry.type = "folder";
      }
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
  button.textContent = label;
  button.addEventListener("click", handler);
  return button;
}

function pill(text) {
  const span = document.createElement("span");
  span.className = "pill";
  span.textContent = text;
  return span;
}

function elapsed(timestamp) {
  if (!timestamp) {
    return "0s";
  }
  const totalSeconds = Math.max(0, Math.floor((Date.now() - Date.parse(timestamp)) / 1000));
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return minutes > 0 ? `${minutes}m ${seconds}s` : `${seconds}s`;
}

function formatTime(timestamp) {
  return new Date(timestamp).toLocaleTimeString();
}

window.addEventListener("DOMContentLoaded", init);
