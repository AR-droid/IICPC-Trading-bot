const express = require('express');
const http = require('http');
const WebSocket = require('ws');
const redis = require('redis');
const path = require('path');

const port = process.env.PORT || 3000;
const redisUrl = process.env.REDIS_URL || 'redis://localhost:6379/0';

const app = express();
const server = http.createServer(app);
const wss = new WebSocket.Server({ server });

// Serve static assets from public folder
app.use(express.static(path.join(__dirname, 'public')));
app.use(express.json());

// Redis setup
const redisClient = redis.createClient({ url: redisUrl });
const redisSub = redisClient.duplicate();

redisClient.on('error', (err) => console.error('Redis Client Error', err));
redisSub.on('error', (err) => console.error('Redis Subscriber Error', err));

async function startRedis() {
  await redisClient.connect();
  console.log('Dashboard connected to Redis successfully.');
  await redisSub.connect();
  console.log('Dashboard duplicate connected for subscribing.');

  // Subscribe to channels
  await redisSub.subscribe('live_metrics', (message) => {
    broadcast({ type: 'metrics', data: JSON.parse(message) });
  });

  await redisSub.subscribe('test_events', (message) => {
    broadcast({ type: 'event', data: JSON.parse(message) });
  });
}

startRedis().catch(err => console.error('Failed to initialize Redis connection', err));

// WebSocket broadcast helper
function broadcast(data) {
  const payload = JSON.stringify(data);
  wss.clients.forEach((client) => {
    if (client.readyState === WebSocket.OPEN) {
      client.send(payload);
    }
  });
}

// WebSocket connections
wss.on('connection', (ws) => {
  console.log('Client connected to WebSocket.');
  ws.send(JSON.stringify({ type: 'system', message: 'Connected to Live telemetry stream' }));
});

// REST API Endpoints
app.get('/api/leaderboard', async (req, res) => {
  try {
    // Fetch members and scores from leaderboard sorted set
    const leaderboard = await redisClient.zRangeWithScores('leaderboard', 0, -1, { REV: true });
    
    // Fetch full results for detailed views
    const results = await redisClient.hGetAll('contestant_results');
    
    const formatted = leaderboard.map((item, index) => {
      let details = {};
      if (results && results[item.value]) {
        try {
          details = JSON.parse(results[item.value]);
        } catch (e) {
          console.error("Error parsing results JSON for", item.value);
        }
      }
      return {
        rank: index + 1,
        team_name: item.value,
        score: parseFloat(item.score.toFixed(2)),
        avg_tps: details.average_tps ? parseFloat(details.average_tps.toFixed(2)) : 0,
        p99_latency: details.p99_latency ? parseFloat(details.p99_latency.toFixed(2)) : 0,
        success_rate: details.success_rate ? parseFloat((details.success_rate * 100).toFixed(2)) : 0,
        correctness_errors: details.correctness_errors || 0
      };
    });
    
    res.json(formatted);
  } catch (err) {
    console.error('Error fetching leaderboard', err);
    res.status(500).json({ error: 'Failed to fetch leaderboard' });
  }
});

app.get('/api/results/:team', async (req, res) => {
  try {
    const { team } = req.params;
    const data = await redisClient.hGet('contestant_results', team.toLowerCase());
    if (!data) {
      return res.status(404).json({ error: 'Result not found' });
    }
    res.json(JSON.parse(data));
  } catch (err) {
    res.status(500).json({ error: 'Failed to fetch result' });
  }
});

// Serve Frontend UI for all other paths
app.get('*', (req, res) => {
  res.sendFile(path.join(__dirname, 'public', 'index.html'));
});

server.listen(port, () => {
  console.log(`Express and WebSocket server running on port ${port}`);
});
