// TunnelX Server Dashboard JavaScript

let updateInterval;

function init() {
    loadToken();
    loadStats();
    loadClients();
    loadProxies();
    loadPendingRequests();

    // 自动刷新
    updateInterval = setInterval(() => {
        loadStats();
        loadClients();
        loadProxies();
        loadPendingRequests();
    }, 3000);
}

function loadToken() {
    fetch('/api/token')
        .then(res => res.json())
        .then(data => {
            document.getElementById('connection-token').value = data.token;
        })
        .catch(err => {
            console.error('Failed to load token:', err);
            document.getElementById('connection-token').value = 'Error loading token';
        });
}

function loadPendingRequests() {
    fetch('/api/pending-requests')
        .then(res => res.json())
        .then(requests => {
            const tbody = document.getElementById('requests-body');

            if (!requests || requests.length === 0) {
                tbody.innerHTML = '<tr><td colspan="5" style="text-align:center;">No pending requests</td></tr>';
                return;
            }

            tbody.innerHTML = requests.map(req => `
                <tr>
                    <td>${escapeHtml(req.client_id)}</td>
                    <td>${req.local_port}</td>
                    <td>${req.proxy_type}</td>
                    <td>${formatTime(req.request_time)}</td>
                    <td>
                        <button class="btn-approve" onclick="approveRequest('${req.id}')">Approve</button>
                        <button class="btn-reject" onclick="rejectRequest('${req.id}')">Reject</button>
                    </td>
                </tr>
            `).join('');
        })
        .catch(err => console.error('Failed to load requests:', err));
}

function approveRequest(requestId) {
    if (!confirm('Approve this proxy request?')) {
        return;
    }

    fetch('/api/approve-request', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ request_id: requestId })
    })
    .then(res => res.json())
    .then(data => {
        if (data.success) {
            alert('Request approved! Remote port: ' + data.remote_port);
            loadPendingRequests();
        } else {
            alert('Failed to approve request');
        }
    })
    .catch(err => {
        console.error('Approve failed:', err);
        alert('Failed to approve request');
    });
}

function rejectRequest(requestId) {
    const reason = prompt('Reject reason:', 'Rejected by administrator');
    if (!reason) {
        return;
    }

    fetch('/api/reject-request', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ request_id: requestId, reason: reason })
    })
    .then(res => res.json())
    .then(data => {
        if (data.success) {
            alert('Request rejected');
            loadPendingRequests();
        } else {
            alert('Failed to reject request');
        }
    })
    .catch(err => {
        console.error('Reject failed:', err);
        alert('Failed to reject request');
    });
}

function copyToken() {
    const tokenInput = document.getElementById('connection-token');
    tokenInput.select();
    document.execCommand('copy');
    alert('Token copied to clipboard!');
}

function loadStats() {
    fetch('/api/stats')
        .then(res => res.json())
        .then(data => {
            document.getElementById('total-clients').textContent = data.total_clients || 0;
            document.getElementById('active-connections').textContent = data.active_connections || 0;
            document.getElementById('bytes-in').textContent = formatBytes(data.total_bytes_in || 0);
            document.getElementById('bytes-out').textContent = formatBytes(data.total_bytes_out || 0);
        })
        .catch(err => console.error('Failed to load stats:', err));
}

function loadClients() {
    fetch('/api/clients')
        .then(res => res.json())
        .then(clients => {
            const tbody = document.getElementById('clients-body');

            if (!clients || clients.length === 0) {
                tbody.innerHTML = '<tr><td colspan="6" style="text-align:center;">No clients connected</td></tr>';
                return;
            }

            tbody.innerHTML = clients.map(client => `
                <tr>
                    <td>${escapeHtml(client.id)}</td>
                    <td>${escapeHtml(client.address)}</td>
                    <td>${formatTime(client.connected_at)}</td>
                    <td>${formatTime(client.last_heartbeat)}</td>
                    <td>${client.proxies_count}</td>
                    <td><button onclick="kickClient('${escapeHtml(client.id)}')">Kick</button></td>
                </tr>
            `).join('');
        })
        .catch(err => console.error('Failed to load clients:', err));
}

function loadProxies() {
    fetch('/api/proxies')
        .then(res => res.json())
        .then(proxies => {
            const tbody = document.getElementById('proxies-body');

            if (!proxies || proxies.length === 0) {
                tbody.innerHTML = '<tr><td colspan="6" style="text-align:center;">No proxies</td></tr>';
                return;
            }

            tbody.innerHTML = proxies.map(proxy => `
                <tr>
                    <td>${escapeHtml(proxy.name)}</td>
                    <td>${proxy.type}</td>
                    <td>${proxy.remote_port}</td>
                    <td>${formatBytes(proxy.bytes_in)}</td>
                    <td>${formatBytes(proxy.bytes_out)}</td>
                    <td>${proxy.connections}</td>
                </tr>
            `).join('');
        })
        .catch(err => console.error('Failed to load proxies:', err));
}

function kickClient(clientId) {
    if (!confirm('Kick client ' + clientId + '?')) {
        return;
    }

    fetch('/api/client/kick', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ client_id: clientId })
    })
    .then(() => {
        loadClients();
    })
    .catch(err => console.error('Kick failed:', err));
}

function formatBytes(bytes) {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
}

function formatTime(timeStr) {
    if (!timeStr) return 'N/A';
    const date = new Date(timeStr);
    return date.toLocaleString();
}

function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

window.onload = init;
