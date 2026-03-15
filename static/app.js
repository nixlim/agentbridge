const state = {
  agents: {},
  tasks: [],
  messages: [],
  search: "",
  agentFilter: "",
  messageTypeFilter: new Set(),
};

const refs = {
  connection: document.getElementById("connection-status"),
  agentsList: document.getElementById("agents-list"),
  tasksList: document.getElementById("tasks-list"),
  messageList: document.getElementById("message-list"),
  searchInput: document.getElementById("search-input"),
  agentFilter: document.getElementById("agent-filter"),
  messageTypes: document.getElementById("message-types"),
  clearMessages: document.getElementById("clear-messages"),
  clearTasks: document.getElementById("clear-tasks"),
  composerForm: document.getElementById("composer-form"),
  sendMode: document.getElementById("send-mode"),
  targetAgent: document.getElementById("target-agent"),
  reviewAgent: document.getElementById("review-agent"),
  taskTitle: document.getElementById("task-title"),
  dependsOn: document.getElementById("depends-on"),
  composerInput: document.getElementById("composer-input"),
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

  refs.searchInput.addEventListener("input", (event) => {
    state.search = event.target.value.toLowerCase();
    renderMessages();
  });

  refs.agentFilter.addEventListener("change", (event) => {
    state.agentFilter = event.target.value;
    renderAgents();
    renderMessages();
  });

  refs.clearMessages.addEventListener("click", clearMessages);
  refs.clearTasks.addEventListener("click", clearFinishedTasks);
  refs.sendMode.addEventListener("change", toggleComposerFields);
  refs.composerForm.addEventListener("submit", submitComposer);
  toggleComposerFields();
  connectWebSocket();
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
        state.agents = payload.data.agents || {};
        state.tasks = payload.data.tasks || [];
        state.messages = payload.data.messages || [];
        populateAgentOptions();
        renderAll();
        break;
      case "message":
        state.messages.push(payload.data);
        renderMessages();
        break;
      case "task_update":
        upsertTask(payload.data);
        renderTasks();
        populateDependsOn();
        break;
      case "agent_status":
        state.agents[payload.data.name] = payload.data;
        populateAgentOptions();
        renderAgents();
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

function renderAll() {
  populateAgentOptions();
  populateDependsOn();
  renderAgents();
  renderTasks();
  renderMessages();
}

function populateAgentOptions() {
  const names = Object.keys(state.agents).sort();
  [refs.agentFilter, refs.targetAgent, refs.reviewAgent].forEach((select, index) => {
    const existing = select.value;
    select.textContent = "";
    if (index === 0) {
      appendOption(select, "", "All agents");
    } else if (index === 2) {
      appendOption(select, "", "No reviewer");
    }
    names.forEach((name) => appendOption(select, name, name));
    if (names.includes(existing) || existing === "") {
      select.value = existing;
    }
  });
}

function populateDependsOn() {
  const ids = state.tasks.map((task) => task.id).join(", ");
  refs.dependsOn.placeholder = ids ? `Depends on task IDs, e.g. ${ids}` : "Depends on task IDs (comma separated)";
}

function appendOption(select, value, label) {
  const option = document.createElement("option");
  option.value = value;
  option.textContent = label;
  select.appendChild(option);
}

function renderAgents() {
  refs.agentsList.textContent = "";
  Object.values(state.agents).sort((a, b) => a.name.localeCompare(b.name)).forEach((agent) => {
    const card = document.createElement("button");
    card.type = "button";
    card.className = "agent-card";
    if (state.agentFilter === agent.name) {
      card.classList.add("active");
    }
    card.addEventListener("click", () => {
      state.agentFilter = state.agentFilter === agent.name ? "" : agent.name;
      refs.agentFilter.value = state.agentFilter;
      renderAgents();
      renderMessages();
    });

    const head = document.createElement("div");
    head.className = "agent-title";
    const name = document.createElement("strong");
    name.textContent = agent.name;
    const status = document.createElement("span");
    const dot = document.createElement("span");
    dot.className = `status-dot status-${agent.status}`;
    status.append(dot, document.createTextNode(` ${agent.status}`));
    head.append(name, status);

    const info = document.createElement("p");
    info.className = "muted";
    info.textContent = `task: ${agent.current_task || "none"} • completed: ${agent.tasks_completed}`;

    const tokens = document.createElement("p");
    tokens.className = "muted";
    tokens.textContent = `tokens in/out: ${agent.total_tokens_in}/${agent.total_tokens_out}`;

    card.append(head, info, tokens);

    if (agent.status === "error") {
      const reset = document.createElement("button");
      reset.type = "button";
      reset.textContent = "Reset";
      reset.addEventListener("click", (event) => {
        event.stopPropagation();
        fetch(`/api/agents/${agent.name}/reset`, { method: "POST" });
      });
      card.appendChild(reset);
    }

    refs.agentsList.appendChild(card);
  });
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
    if (state.search && !message.content.toLowerCase().includes(state.search)) {
      return false;
    }
    return true;
  }).slice(-500);

  filtered.forEach((message) => {
    const card = document.createElement("article");
    card.className = "message-card";

    const head = document.createElement("div");
    head.className = "message-head";
    const title = document.createElement("strong");
    title.textContent = `${formatTime(message.timestamp)} ${message.from} → ${message.to}`;
    title.style.color = colorForParticipant(message.from);
    const badge = document.createElement("span");
    badge.className = "badge";
    badge.textContent = message.type;
    head.append(title, badge);

    const body = document.createElement("pre");
    body.textContent = message.content;

    card.append(head, body);

    if (message.task_id) {
      const taskTag = document.createElement("p");
      taskTag.className = "muted";
      taskTag.textContent = `task: ${message.task_id}`;
      card.appendChild(taskTag);
    }

    if (message.metadata && (message.metadata.tokens_in || message.metadata.duration_ms || message.metadata.error)) {
      const meta = document.createElement("p");
      meta.className = "muted";
      meta.textContent = [
        message.metadata.tokens_in ? `tokens in ${message.metadata.tokens_in}` : "",
        message.metadata.tokens_out ? `tokens out ${message.metadata.tokens_out}` : "",
        message.metadata.duration_ms ? `duration ${message.metadata.duration_ms}ms` : "",
        message.metadata.error ? `error ${message.metadata.error}` : "",
      ].filter(Boolean).join(" • ");
      card.appendChild(meta);
    }

    refs.messageList.appendChild(card);
  });
  refs.messageList.scrollTop = refs.messageList.scrollHeight;
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
    if (task.status === "blocked") {
      groups.pending.push(task);
      return;
    }
    if (groups[task.status]) {
      groups[task.status].push(task);
    }
  });

  Object.entries(groups).forEach(([status, tasks]) => {
    if (tasks.length === 0) {
      return;
    }
    const group = document.createElement("section");
    group.className = "task-group";
    const header = document.createElement("strong");
    header.textContent = `${status} (${tasks.length})`;
    group.appendChild(header);

    tasks.forEach((task) => {
      const card = document.createElement("article");
      card.className = "task-card";

      const head = document.createElement("div");
      head.className = "task-head";
      const title = document.createElement("strong");
      title.textContent = task.title;
      const badge = document.createElement("span");
      badge.className = "badge";
      badge.textContent = `${task.assigned_to} • ${task.status}`;
      head.append(title, badge);

      const desc = document.createElement("pre");
      desc.textContent = task.description;
      const deps = document.createElement("p");
      deps.className = "muted";
      deps.textContent = task.depends_on && task.depends_on.length ? `depends on: ${task.depends_on.join(", ")}` : "no dependencies";

      card.append(head, desc, deps);

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

      if (task.status === "review") {
        const actions = document.createElement("div");
        actions.className = "task-actions";
        const approve = document.createElement("button");
        approve.type = "button";
        approve.textContent = "Approve";
        approve.addEventListener("click", () => {
          fetch(`/api/tasks/${task.id}/approve`, { method: "POST" });
        });
        const reject = document.createElement("button");
        reject.type = "button";
        reject.textContent = "Reject";
        reject.addEventListener("click", () => {
          const reason = window.prompt("Reason for rejection");
          if (reason) {
            fetch(`/api/tasks/${task.id}/reject`, {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({ reason }),
            });
          }
        });
        actions.append(approve, reject);
        card.appendChild(actions);
      }

      group.appendChild(card);
    });
    refs.tasksList.appendChild(group);
  });
}

function toggleComposerFields() {
  const isTask = refs.sendMode.value === "task";
  refs.taskTitle.style.display = isTask ? "block" : "none";
  refs.reviewAgent.style.display = isTask ? "block" : "none";
  refs.dependsOn.style.display = isTask ? "block" : "none";
}

function submitComposer(event) {
  event.preventDefault();
  if (refs.sendMode.value === "task") {
    fetch("/api/tasks", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        title: refs.taskTitle.value,
        description: refs.composerInput.value,
        assigned_to: refs.targetAgent.value,
        review_by: refs.reviewAgent.value,
        depends_on: refs.dependsOn.value.split(",").map((value) => value.trim()).filter(Boolean),
      }),
    });
  } else {
    fetch("/api/messages", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        to: refs.targetAgent.value,
        content: refs.composerInput.value,
      }),
    });
  }
  refs.composerInput.value = "";
  refs.taskTitle.value = "";
  refs.dependsOn.value = "";
}

function clearMessages() {
  fetch("/api/messages/clear", { method: "POST" });
}

function clearFinishedTasks() {
  fetch("/api/tasks/clear", { method: "POST" });
}

function upsertTask(task) {
  const index = state.tasks.findIndex((entry) => entry.id === task.id);
  if (index >= 0) {
    state.tasks[index] = task;
  } else {
    state.tasks.push(task);
  }
}

function formatTime(timestamp) {
  return new Date(timestamp).toLocaleTimeString();
}

function colorForParticipant(name) {
  if (name === "claude") return "var(--claude)";
  if (name === "codex") return "var(--codex)";
  if (name === "human") return "var(--human)";
  return "var(--coord)";
}

window.addEventListener("DOMContentLoaded", init);
