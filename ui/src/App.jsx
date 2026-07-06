import { useState, useEffect } from 'react';
import './index.css';

function App() {
  const [authHeader, setAuthHeader] = useState(localStorage.getItem('llm_basic_auth') || '');
  const [isAuthenticated, setIsAuthenticated] = useState(!!localStorage.getItem('llm_basic_auth'));
  
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  
  const [metrics, setMetrics] = useState(null);
  const [logs, setLogs] = useState([]);
  const [keys, setKeys] = useState([]);
  const [models, setModels] = useState([]);
  
  // For interacting with /api/v1 endpoints which require an API key
  const [activeApiKey, setActiveApiKey] = useState(localStorage.getItem('llm_active_key') || '');
  const [chatModel, setChatModel] = useState('');
  const [chatInput, setChatInput] = useState('');
  const [chatResponse, setChatResponse] = useState('');
  const [isChatting, setIsChatting] = useState(false);

  const [error, setError] = useState('');
  const [successMsg, setSuccessMsg] = useState('');

  const fetchDashboardData = async () => {
    try {
      const headers = { 'Authorization': `Basic ${authHeader}` };
      
      const metricsRes = await fetch('/admin/metrics', { headers });
      if (metricsRes.status === 401 || metricsRes.status === 403) {
        handleLogout();
        setError('Invalid Admin Credentials');
        return;
      }
      const metricsData = await metricsRes.json();
      setMetrics(metricsData);

      const logsRes = await fetch('/admin/logs', { headers });
      const logsData = await logsRes.json();
      setLogs(logsData || []);

      const keysRes = await fetch('/admin/keys', { headers });
      const keysData = await keysRes.json();
      setKeys(keysData || []);
      setError('');
    } catch (err) {
      console.error(err);
      setError('Failed to fetch data from the server.');
    }
  };

  const fetchModels = async () => {
    if (!activeApiKey) return;
    try {
      const res = await fetch('/api/v1/models', {
        headers: { 'Authorization': `Bearer ${activeApiKey}` }
      });
      if (res.status === 200) {
        const data = await res.json();
        setModels(data.models || []);
        if (data.models && data.models.length > 0 && !chatModel) {
          setChatModel(data.models[0].name);
        }
      }
    } catch (err) {
      console.error("Failed to fetch models", err);
    }
  };

  useEffect(() => {
    if (isAuthenticated) {
      fetchDashboardData();
      const interval = setInterval(fetchDashboardData, 5000);
      return () => clearInterval(interval);
    }
  }, [isAuthenticated, authHeader]);

  useEffect(() => {
    if (isAuthenticated && activeApiKey) {
      fetchModels();
    }
  }, [isAuthenticated, activeApiKey]);

  const handleLogin = (e) => {
    e.preventDefault();
    if (username.trim() && password.trim()) {
      const encoded = btoa(`${username.trim()}:${password.trim()}`);
      localStorage.setItem('llm_basic_auth', encoded);
      setAuthHeader(encoded);
      setIsAuthenticated(true);
      setError('');
    }
  };

  const handleLogout = () => {
    localStorage.removeItem('llm_basic_auth');
    setAuthHeader('');
    setIsAuthenticated(false);
    setMetrics(null);
    setLogs([]);
    setKeys([]);
  };

  const generateKey = async () => {
    try {
      const res = await fetch('/admin/keys', {
        method: 'POST',
        headers: {
          'Authorization': `Basic ${authHeader}`,
          'Content-Type': 'application/json'
        },
        body: JSON.stringify({ user_id: 1, role_id: 1, rate_limit_rpm: 120 })
      });
      if (res.status === 201) {
        const data = await res.json();
        setSuccessMsg(`Save this Key: ${data.plain_api_key}`);
        fetchDashboardData();
      } else {
        setError('Failed to generate key');
      }
    } catch (err) {
      setError('Error generating key');
    }
  };

  const deleteKey = async (id) => {
    try {
      const res = await fetch(`/admin/keys/${id}`, {
        method: 'DELETE',
        headers: { 'Authorization': `Basic ${authHeader}` }
      });
      if (res.status === 200) {
        fetchDashboardData();
      } else {
        setError('Failed to delete key');
      }
    } catch (err) {
      setError('Error deleting key');
    }
  };

  const setAsActiveKey = () => {
    const key = prompt("Enter the plain API Key string to use for testing:");
    if (key) {
      localStorage.setItem('llm_active_key', key);
      setActiveApiKey(key);
    }
  };

  const sendChatMessage = async (e) => {
    e.preventDefault();
    if (!chatInput.trim() || !chatModel || !activeApiKey) return;
    
    setIsChatting(true);
    setChatResponse('');
    
    try {
      const res = await fetch('/api/v1/chat', {
        method: 'POST',
        headers: {
          'Authorization': `Bearer ${activeApiKey}`,
          'Content-Type': 'application/json'
        },
        body: JSON.stringify({
          model: chatModel,
          messages: [{ role: "user", content: chatInput }],
          stream: false
        })
      });
      
      const data = await res.json();
      if (res.status === 200) {
        setChatResponse(data.message?.content || "No content returned");
      } else {
        setChatResponse(`Error: ${data.error || 'Failed to get response'}`);
      }
    } catch (err) {
      setChatResponse(`Error: ${err.message}`);
    }
    setIsChatting(false);
  };

  if (!isAuthenticated) {
    return (
      <div className="auth-container">
        <h2>Admin Login</h2>
        <form onSubmit={handleLogin}>
          <input 
            type="text" 
            className="input-styled" 
            placeholder="Admin Username" 
            value={username}
            onChange={(e) => setUsername(e.target.value)}
          />
          <input 
            type="password" 
            className="input-styled" 
            placeholder="Admin Password" 
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
          {error && <p className="status-error" style={{ padding: '8px', borderRadius: '4px' }}>{error}</p>}
          <button type="submit" className="btn-styled" style={{ width: '100%' }}>Login</button>
        </form>
      </div>
    );
  }

  return (
    <div className="app-container">
      <div className="header">
        <h1>LLM Gateway Developer Portal</h1>
        <button className="btn-styled" onClick={handleLogout} style={{ background: '#6c757d' }}>Logout</button>
      </div>

      {error && <div className="panel status-error">{error}</div>}
      {successMsg && <div className="panel status-success" style={{ padding: '15px' }}><strong>{successMsg}</strong></div>}

      <div className="panel">
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '16px', borderBottom: '1px solid #eee', paddingBottom: '8px' }}>
          <h3 style={{ border: 'none', margin: 0, padding: 0 }}>API Key Management</h3>
          <button className="btn-styled" onClick={generateKey}>+ Generate Key</button>
        </div>
        <div style={{ overflowX: 'auto' }}>
          <table>
            <thead>
              <tr>
                <th>ID</th>
                <th>Prefix</th>
                <th>Role ID</th>
                <th>RPM Limit</th>
                <th>Created</th>
                <th>Action</th>
              </tr>
            </thead>
            <tbody>
              {keys.length === 0 ? (
                <tr><td colSpan="6" style={{ textAlign: 'center' }}>No API keys found.</td></tr>
              ) : (
                keys.map(k => (
                  <tr key={k.id}>
                    <td>{k.id}</td>
                    <td style={{ fontFamily: 'monospace' }}>{k.prefix}</td>
                    <td>{k.role_id}</td>
                    <td>{k.rate_limit_rpm}</td>
                    <td>{new Date(k.created_at).toLocaleString()}</td>
                    <td>
                      <button 
                        onClick={() => deleteKey(k.id)} 
                        style={{ background: '#dc3545', color: 'white', border: 'none', padding: '6px 12px', borderRadius: '4px', cursor: 'pointer', fontSize: '0.8rem' }}
                      >Revoke</button>
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      </div>

      <div className="panel">
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '16px', borderBottom: '1px solid #eee', paddingBottom: '8px' }}>
          <h3 style={{ border: 'none', margin: 0, padding: 0 }}>Chatbot Interface</h3>
          <div>
            {!activeApiKey ? (
              <span style={{ color: '#dc3545', fontSize: '0.9rem', marginRight: '10px' }}>No Active API Key Set</span>
            ) : (
              <span style={{ color: '#28a745', fontSize: '0.9rem', marginRight: '10px' }}>Testing Key Enabled</span>
            )}
            <button className="btn-styled" onClick={setAsActiveKey} style={{ fontSize: '0.8rem', padding: '6px 12px' }}>Set Testing API Key</button>
          </div>
        </div>

        {activeApiKey ? (
          <div>
            <div style={{ marginBottom: '10px' }}>
              <label style={{ display: 'block', marginBottom: '5px', fontSize: '0.9rem', color: '#555' }}>Select Model</label>
              <select 
                className="input-styled" 
                value={chatModel} 
                onChange={(e) => setChatModel(e.target.value)}
              >
                <option value="">-- Select a model --</option>
                {models.map(m => (
                  <option key={m.name} value={m.name}>{m.name} ({Math.round(m.size/1024/1024)} MB)</option>
                ))}
              </select>
            </div>
            <form onSubmit={sendChatMessage}>
              <label style={{ display: 'block', marginBottom: '5px', fontSize: '0.9rem', color: '#555' }}>Message</label>
              <textarea 
                className="input-styled" 
                rows="3" 
                placeholder="Ask the LLM something..."
                value={chatInput}
                onChange={(e) => setChatInput(e.target.value)}
                style={{ resize: 'vertical' }}
              />
              <button type="submit" className="btn-styled" disabled={isChatting || !chatModel}>
                {isChatting ? 'Sending...' : 'Send Message'}
              </button>
            </form>
            {chatResponse && (
              <div style={{ marginTop: '20px' }}>
                <label style={{ display: 'block', marginBottom: '5px', fontSize: '0.9rem', color: '#555' }}>Response</label>
                <pre style={{ whiteSpace: 'pre-wrap' }}>{chatResponse}</pre>
              </div>
            )}
          </div>
        ) : (
          <p style={{ color: '#666', fontSize: '0.9rem' }}>Please set a testing API key to use the chat interface.</p>
        )}
      </div>

      {metrics && (
        <div className="panel">
          <h3>System Metrics</h3>
          <div className="metrics-grid">
            <div className="metric-card">
              <span className="metric-label">Total Requests</span>
              <span className="metric-value">{metrics.total_requests}</span>
            </div>
            <div className="metric-card">
              <span className="metric-label">Avg Latency (ms)</span>
              <span className="metric-value">{Math.round(metrics.average_latency_ms)}</span>
            </div>
            <div className="metric-card">
              <span className="metric-label">Active Workers</span>
              <span className="metric-value">{metrics.active_workers}</span>
            </div>
          </div>
        </div>
      )}

      <div className="panel">
        <h3>Recent Request Logs</h3>
        <div style={{ overflowX: 'auto' }}>
          <table>
            <thead>
              <tr>
                <th>ID</th>
                <th>Status</th>
                <th>Model</th>
                <th>Latency</th>
                <th>IP Address</th>
                <th>Time</th>
              </tr>
            </thead>
            <tbody>
              {logs.length === 0 ? (
                <tr><td colSpan="6" style={{ textAlign: 'center' }}>No logs found.</td></tr>
              ) : (
                logs.map(log => (
                  <tr key={log.id}>
                    <td style={{ fontFamily: 'monospace' }}>{log.request_id.substring(0,8)}</td>
                    <td>
                      <span className={`status-badge ${log.status_code === 200 ? 'status-success' : 'status-error'}`}>
                        {log.status_code}
                      </span>
                    </td>
                    <td>{log.model}</td>
                    <td>{log.latency_ms} ms</td>
                    <td>{log.ip_address}</td>
                    <td>{new Date(log.created_at).toLocaleString()}</td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}

export default App;
