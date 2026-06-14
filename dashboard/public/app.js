// Dynamically resolve sandbox API based on current host (works both locally and in cloud VM)
const SANDBOX_API = `${window.location.protocol}//${window.location.hostname}:8000`;
const BACKEND_API = window.location.origin;

// State Variables
let socket;
let liveChart;
let testTimer;
let testStartTime;
let testDuration = 30;
let chartLabels = [];
let chartTPSData = [];
let chartP99Data = [];
let submittedTeams = new Set();

// DOM Elements
const wsStatus = document.getElementById('ws-status');
const apiStatus = document.getElementById('api-status');
const submissionForm = document.getElementById('submission-form');
const teamNameInput = document.getElementById('team-name-input');
const fileInput = document.getElementById('file-input');
const dropzone = document.getElementById('dropzone');
const dropzoneText = document.getElementById('dropzone-text');
const submitBtn = document.getElementById('submit-btn');
const submitLogs = document.getElementById('submit-logs');

const testForm = document.getElementById('test-form');
const testTeamSelect = document.getElementById('test-team-select');
const botCountInput = document.getElementById('bot-count-input');
const durationInput = document.getElementById('duration-input');
const startTestBtn = document.getElementById('start-test-btn');
const testStatusMsg = document.getElementById('test-status-msg');

const activeTestBanner = document.getElementById('active-test-banner');
const bannerTeamName = document.getElementById('banner-team-name');
const bannerTimer = document.getElementById('banner-timer');
const liveTPS = document.getElementById('live-tps');
const liveP50 = document.getElementById('live-p50');
const liveP99 = document.getElementById('live-p99');
const liveCorrectness = document.getElementById('live-correctness');
const tradeTape = document.getElementById('trade-tape');
const hudSpreadValue = document.getElementById('hud-spread-value');
const hudBestBid = document.getElementById('hud-best-bid');
const hudBestAsk = document.getElementById('hud-best-ask');

const leaderboardBody = document.getElementById('leaderboard-body');
const refreshLeaderboardBtn = document.getElementById('refresh-leaderboard');

// Initialize WebSockets
function initWebSocket() {
  const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  const wsUrl = `${wsProtocol}//${window.location.host}`;
  
  socket = new WebSocket(wsUrl);
  
  socket.onopen = () => {
    wsStatus.textContent = 'WS Online';
    wsStatus.className = 'badge badge-connected';
    console.log('WebSocket connection established.');
  };
  
  socket.onclose = () => {
    wsStatus.textContent = 'WS Offline';
    wsStatus.className = 'badge badge-disconnected';
    console.log('WebSocket connection closed. Reconnecting in 3s...');
    setTimeout(initWebSocket, 3000);
  };
  
  socket.onerror = (error) => {
    console.error('WebSocket Error:', error);
  };
  
  socket.onmessage = (event) => {
    const message = JSON.parse(event.data);
    
    if (message.type === 'metrics') {
      updateLiveMetrics(message.data);
    } else if (message.type === 'event') {
      handleBenchmarkingEvent(message.data);
    }
  };
}

// Check Health of Sandbox API
async function checkApiHealth() {
  try {
    const res = await fetch(`${SANDBOX_API}/health`);
    if (res.ok) {
      apiStatus.textContent = 'API Online';
      apiStatus.className = 'badge badge-connected';
    } else {
      throw new Error();
    }
  } catch (e) {
    apiStatus.textContent = 'API Offline';
    apiStatus.className = 'badge badge-disconnected';
  }
}

// Fetch Leaderboard
async function fetchLeaderboard() {
  try {
    const res = await fetch(`${BACKEND_API}/api/leaderboard`);
    if (!res.ok) throw new Error('Failed to fetch leaderboard');
    
    const data = await res.json();
    renderLeaderboard(data);
    updateTeamSelect(data);
  } catch (err) {
    console.error('Error fetching leaderboard:', err);
  }
}

// Render Leaderboard Table
function renderLeaderboard(data) {
  if (data.length === 0) {
    leaderboardBody.innerHTML = `
      <tr>
        <td colspan="7" class="loading-state">No matching engines benchmarked yet. Submit code to begin.</td>
      </tr>
    `;
    return;
  }
  
  leaderboardBody.innerHTML = '';
  data.forEach((row) => {
    const rankClass = row.rank <= 3 ? `rank-${row.rank}` : '';
    const correctnessClass = row.correctness_errors === 0 ? 'correctness-perfect' : 'correctness-flawed';
    const correctnessText = row.correctness_errors === 0 ? '✓ Perfect' : `✗ ${row.correctness_errors} Errors`;
    
    const tr = document.createElement('tr');
    tr.innerHTML = `
      <td><span class="rank-badge ${rankClass}">${row.rank}</span></td>
      <td class="team-name-td">${row.team_name.toUpperCase()}</td>
      <td class="score-td">${row.score}</td>
      <td>${row.avg_tps}</td>
      <td class="latency-td">${row.p99_latency} ms</td>
      <td>${row.success_rate}%</td>
      <td class="correctness-td ${correctnessClass}">${correctnessText}</td>
    `;
    leaderboardBody.appendChild(tr);
  });
}

// Update the select box for test triggers
function updateTeamSelect(data) {
  // Store all known teams
  data.forEach(item => submittedTeams.add(item.team_name));
  
  const currentSelect = testTeamSelect.value;
  testTeamSelect.innerHTML = '<option value="" disabled>Select a team...</option>';
  
  submittedTeams.forEach((team) => {
    const option = document.createElement('option');
    option.value = team;
    option.textContent = team.toUpperCase();
    testTeamSelect.appendChild(option);
  });
  
  if (submittedTeams.has(currentSelect)) {
    testTeamSelect.value = currentSelect;
  } else {
    testTeamSelect.selectedIndex = 0;
  }
}

// File Dropzone Handlers
dropzone.addEventListener('click', () => fileInput.click());

dropzone.addEventListener('dragover', (e) => {
  e.preventDefault();
  dropzone.classList.add('dragover');
});

dropzone.addEventListener('dragleave', () => {
  dropzone.classList.remove('dragover');
});

dropzone.addEventListener('drop', (e) => {
  e.preventDefault();
  dropzone.classList.remove('dragover');
  if (e.dataTransfer.files.length > 0) {
    const file = e.dataTransfer.files[0];
    if (file.name.endsWith('.go')) {
      fileInput.files = e.dataTransfer.files;
      dropzoneText.textContent = file.name;
    } else {
      alert('Only .go files are allowed!');
    }
  }
});

fileInput.addEventListener('change', () => {
  if (fileInput.files.length > 0) {
    dropzoneText.textContent = fileInput.files[0].name;
  }
});

// Submit Form Handler
submissionForm.addEventListener('submit', async (e) => {
  e.preventDefault();
  const teamName = teamNameInput.value.trim().toLowerCase();
  const file = fileInput.files[0];
  
  if (!teamName || !file) return;
  
  submitBtn.disabled = true;
  submitBtn.textContent = 'Building Docker Image...';
  submitLogs.classList.remove('hidden');
  submitLogs.classList.remove('error');
  submitLogs.textContent = 'Uploading source code and initiating container build...\n';
  
  const formData = new FormData();
  formData.append('team_name', teamName);
  formData.append('file', file);
  
  try {
    const res = await fetch(`${SANDBOX_API}/submit`, {
      method: 'POST',
      body: formData
    });
    
    const data = await res.json();
    
    if (res.ok) {
      submitLogs.textContent += `Success: ${data.message}\nSubmitting build output done. Ready for test!`;
      submittedTeams.add(teamName);
      
      // Auto update selectors
      const opt = document.createElement('option');
      opt.value = teamName;
      opt.textContent = teamName.toUpperCase();
      testTeamSelect.appendChild(opt);
      testTeamSelect.value = teamName;
      
      // Flash green border
      submitLogs.style.borderColor = 'var(--color-success)';
    } else {
      throw new Error(data.detail?.logs || data.detail || 'Build Error');
    }
  } catch (err) {
    console.error(err);
    submitLogs.classList.add('error');
    submitLogs.textContent = `Build Failed:\n\n${err.message}`;
    submitLogs.style.borderColor = 'var(--color-error)';
  } finally {
    submitBtn.disabled = false;
    submitBtn.textContent = 'Build Submission';
  }
});

// Launch Test Form Handler
testForm.addEventListener('submit', async (e) => {
  e.preventDefault();
  const teamName = testTeamSelect.value;
  const botCount = parseInt(botCountInput.value);
  const duration = parseInt(durationInput.value);
  
  if (!teamName) return;
  
  startTestBtn.disabled = true;
  startTestBtn.textContent = 'Deploying...';
  testStatusMsg.classList.remove('hidden');
  testStatusMsg.className = 'status-msg';
  testStatusMsg.textContent = `Spinning up sandbox container for ${teamName.toUpperCase()}...`;
  
  try {
    // 1. Start Sandbox container
    const startRes = await fetch(`${SANDBOX_API}/start/${teamName}`, { method: 'POST' });
    const startData = await startRes.json();
    
    if (!startRes.ok) throw new Error(startData.detail || 'Failed to start sandbox container.');
    
    testStatusMsg.textContent = `Container active at ${startData.endpoint}. Triggering load generators...`;
    
    // Wait 2 seconds for container warmup
    await new Promise(r => setTimeout(r, 2000));
    
    // 2. Start load test
    const formData = new FormData();
    formData.append('team_name', teamName);
    formData.append('bot_count', botCount);
    formData.append('duration_seconds', duration);
    
    const testRes = await fetch(`${SANDBOX_API}/start-test`, {
      method: 'POST',
      body: formData
    });
    const testData = await testRes.json();
    
    if (!testRes.ok) throw new Error(testData.detail || 'Failed to queue load test.');
    
    testStatusMsg.className = 'status-msg success';
    testStatusMsg.textContent = `Benchmarking run successfully started for ${teamName.toUpperCase()}!`;
    testDuration = duration;
  } catch (err) {
    console.error(err);
    testStatusMsg.className = 'status-msg error';
    testStatusMsg.textContent = `Error: ${err.message}`;
    
    // Attempt stop on failure
    fetch(`${SANDBOX_API}/stop/${teamName}`, { method: 'POST' }).catch(() => {});
    startTestBtn.disabled = false;
    startTestBtn.textContent = 'Launch Stress Test';
  }
});

// WebSocket Event Handler
function handleBenchmarkingEvent(payload) {
  const event = payload.event;
  const data = payload.data;
  
  if (event === 'started') {
    // Reveal Active Stress Test Banner
    activeTestBanner.classList.remove('hidden');
    bannerTeamName.textContent = data.team_name.toUpperCase();
    
    // Reset Counters
    liveTPS.textContent = '0';
    liveP50.textContent = '0.00 ms';
    liveP99.textContent = '0.00 ms';
    liveCorrectness.textContent = '0';
    
    if (tradeTape) {
      tradeTape.innerHTML = `
        <div class="tape-line system-line">[SYSTEM] Initializing bot fleet simulation...</div>
        <div class="tape-line system-line">[SYSTEM] Subscribing to live telemetry...</div>
      `;
    }
    
    // Initialize Chart
    chartLabels = [];
    chartTPSData = [];
    chartP99Data = [];
    initChart();
    
    // Setup Timer
    testStartTime = Date.now();
    testDuration = data.duration;
    startTimer();
    
    // Lock controls
    startTestBtn.disabled = true;
    startTestBtn.textContent = 'Testing in progress...';
  } else if (event === 'finished') {
    // Stop Timer
    clearInterval(testTimer);
    bannerTimer.textContent = 'FINISHED';
    
    if (tradeTape) {
      const div = document.createElement('div');
      div.className = 'tape-line system-line';
      div.textContent = `[SYSTEM] Benchmarking completed for ${data.team_name.toUpperCase()}! Score: ${data.results?.score?.toFixed(2)}`;
      tradeTape.appendChild(div);
      tradeTape.scrollTop = tradeTape.scrollHeight;
    }
    
    // Flash status message
    testStatusMsg.className = 'status-msg success';
    testStatusMsg.textContent = `Benchmarking finished for ${data.team_name.toUpperCase()}! Score: ${data.results?.score?.toFixed(2)}`;
    
    // Stop Container
    setTimeout(async () => {
      activeTestBanner.classList.add('hidden');
      startTestBtn.disabled = false;
      startTestBtn.textContent = 'Launch Stress Test';
      
      // Teardown the contestant container automatically
      await fetch(`${SANDBOX_API}/stop/${data.team_name}`, { method: 'POST' });
      fetchLeaderboard();
    }, 4000);
  }
}

// Update Live Metrics during stress test
function updateLiveMetrics(metrics) {
  liveTPS.textContent = metrics.tps;
  liveP50.innerHTML = `${metrics.p50.toFixed(2)} <span class="unit">ms</span>`;
  liveP99.innerHTML = `${metrics.p99.toFixed(2)} <span class="unit">ms</span>`;
  liveCorrectness.textContent = metrics.correctness_errors;
  
  // Update IMC Prosperity elements
  updateOrderBookHUD(metrics.tps);
  appendTradeTapeLines(metrics.tps);
  
  // Push metrics to graph
  const relativeTime = Math.round((Date.now() - testStartTime) / 1000);
  chartLabels.push(`${relativeTime}s`);
  chartTPSData.push(metrics.tps);
  chartP99Data.push(metrics.p99);
  
  // Keep last 60 points
  if (chartLabels.length > 60) {
    chartLabels.shift();
    chartTPSData.shift();
    chartP99Data.shift();
  }
  
  if (liveChart) {
    liveChart.update();
  }
}

// Countdown timer helper
function startTimer() {
  clearInterval(testTimer);
  testTimer = setInterval(() => {
    const elapsed = Math.floor((Date.now() - testStartTime) / 1000);
    const remaining = Math.max(0, testDuration - elapsed);
    
    const minutes = String(Math.floor(remaining / 60)).padStart(2, '0');
    const seconds = String(remaining % 60).padStart(2, '0');
    
    bannerTimer.textContent = `${minutes}:${seconds}`;
    
    if (remaining <= 0) {
      clearInterval(testTimer);
    }
  }, 1000);
}

// Chart.js initialization
function initChart() {
  const ctx = document.getElementById('liveChart').getContext('2d');
  if (liveChart) {
    liveChart.destroy();
  }
  
  liveChart = new Chart(ctx, {
    type: 'line',
    data: {
      labels: chartLabels,
      datasets: [
        {
          label: 'Throughput (TPS)',
          data: chartTPSData,
          borderColor: 'rgb(0, 229, 255)',
          backgroundColor: 'rgba(0, 229, 255, 0.05)',
          borderWidth: 2,
          yAxisID: 'y-tps',
          tension: 0.3,
          fill: true
        },
        {
          label: 'p99 Latency (ms)',
          data: chartP99Data,
          borderColor: 'rgb(245, 158, 11)',
          backgroundColor: 'transparent',
          borderWidth: 2,
          yAxisID: 'y-latency',
          tension: 0.3
        }
      ]
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      scales: {
        x: {
          grid: { display: false },
          ticks: { color: 'rgba(255,255,255,0.5)' }
        },
        'y-tps': {
          type: 'linear',
          position: 'left',
          title: { display: true, text: 'TPS', color: 'rgb(0, 229, 255)' },
          grid: { color: 'rgba(255,255,255,0.05)' },
          ticks: { color: 'rgba(255,255,255,0.5)' }
        },
        'y-latency': {
          type: 'linear',
          position: 'right',
          title: { display: true, text: 'Latency (ms)', color: 'rgb(245, 158, 11)' },
          grid: { display: false },
          ticks: { color: 'rgba(255,255,255,0.5)' }
        }
      },
      plugins: {
        legend: {
          labels: { color: '#fff', font: { family: 'Outfit' } }
        }
      }
    }
  });
}

// Attach Refresh Leaderboard Action
refreshLeaderboardBtn.addEventListener('click', fetchLeaderboard);

// Initial Page Load
window.addEventListener('DOMContentLoaded', () => {
  initWebSocket();
  checkApiHealth();
  fetchLeaderboard();
  
  // Health check API every 10 seconds
  setInterval(checkApiHealth, 10000);
});

// IMC Prosperity Visualizer simulation functions
function updateOrderBookHUD(tps) {
  if (!hudSpreadValue) return;
  // Generate random order book changes centered around a mid price of 100.00
  const mid = 100.00 + (Math.sin(Date.now() / 5000) * 2.0); // slow oscillation
  
  // Calculate spread (e.g. 0.10 to 0.40)
  const spread = 0.10 + (Math.random() * 0.30);
  hudSpreadValue.textContent = `$${spread.toFixed(2)}`;

  const bestBid = mid - (spread / 2);
  const bestAsk = mid + (spread / 2);

  // Update large HUD metrics
  if (hudBestBid) hudBestBid.textContent = bestBid.toFixed(2);
  if (hudBestAsk) hudBestAsk.textContent = bestAsk.toFixed(2);
  
  // Update asks (above mid)
  for (let i = 0; i < 5; i++) {
    const price = bestAsk + (i * 0.10);
    // Quantity scales loosely with TPS to look active
    const qty = Math.round(10 + Math.random() * (tps / 10 + 20));
    const width = Math.min(100, (qty / 200) * 100);
    
    const row = document.getElementById(`ask-level-${i}`);
    if (row) {
      row.querySelector('.price').textContent = price.toFixed(2);
      row.querySelector('.qty').textContent = qty;
      row.querySelector('.depth-bar').style.width = `${width}%`;
    }
  }
  
  // Update bids (below mid)
  for (let i = 0; i < 5; i++) {
    const price = bestBid - (i * 0.10);
    const qty = Math.round(10 + Math.random() * (tps / 10 + 20));
    const width = Math.min(100, (qty / 200) * 100);
    
    const row = document.getElementById(`bid-level-${i}`);
    if (row) {
      row.querySelector('.price').textContent = price.toFixed(2);
      row.querySelector('.qty').textContent = qty;
      row.querySelector('.depth-bar').style.width = `${width}%`;
    }
  }
}

function appendTradeTapeLines(tps) {
  if (!tradeTape) return;
  
  const timestamp = new Date().toLocaleTimeString();
  const numLines = Math.min(5, Math.ceil(tps / 200) || 1); // scale number of logs with speed
  
  for (let i = 0; i < numLines; i++) {
    const type = Math.random();
    let lineText = '';
    let className = '';
    
    if (type < 0.5) {
      // MATCH
      const side = Math.random() > 0.5 ? 'BUY' : 'SELL';
      const price = 98.00 + Math.random() * 4.0;
      const qty = Math.round(Math.random() * 80) + 5;
      const orderId1 = `ord_${Math.round(Math.random() * 1000)}`;
      const orderId2 = `ord_${Math.round(Math.random() * 1000)}`;
      lineText = `[${timestamp}] MATCH: ${side} ${qty} @ $${price.toFixed(2)} (${orderId1} vs ${orderId2})`;
      className = side === 'BUY' ? 'buy-line' : 'sell-line';
    } else if (type < 0.8) {
      // SUBMIT LIMIT
      const side = Math.random() > 0.5 ? 'BUY' : 'SELL';
      const price = 98.00 + Math.random() * 4.0;
      const qty = Math.round(Math.random() * 50) + 1;
      const orderId = `ord_${Math.round(Math.random() * 1000)}`;
      lineText = `[${timestamp}] SUBMIT: LIMIT ${side} ${qty} @ $${price.toFixed(2)} (${orderId})`;
      className = 'system-line';
    } else {
      // CANCEL
      const orderId = `ord_${Math.round(Math.random() * 1000)}`;
      lineText = `[${timestamp}] CANCEL: ${orderId} successfully deleted`;
      className = 'cancel-line';
    }
    
    const div = document.createElement('div');
    div.className = `tape-line ${className}`;
    div.textContent = lineText;
    tradeTape.appendChild(div);
  }
  
  // Scroll to bottom
  tradeTape.scrollTop = tradeTape.scrollHeight;
  
  // Keep last 40 lines
  while (tradeTape.childElementCount > 40) {
    tradeTape.removeChild(tradeTape.firstChild);
  }
}
