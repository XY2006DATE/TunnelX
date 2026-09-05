// 刷新统计信息
function refreshStats() {
    fetch('/api/stats')
        .then(res => res.json())
        .then(data => {
            document.getElementById('total-clients').textContent = data.total_clients;
            document.getElementById('active-connections').textContent = data.active_connections;
            document.getElementById('bytes-in').textContent = formatBytes(data.total_bytes_in);
            document.getElementById('bytes-out').textContent = formatBytes(data.total_bytes_out);
        })
        .catch(err => console.error('Failed to fetch stats:', err));
}

// 刷新客户端列表
function refreshClients() {
    fetch('/api/clients')
        .then(res => res.json())
        .then(data => {
            const tbody = document.querySelector('#clients-table tbody');

            if (data.length === 0) {
                tbody.innerHTML = '<tr><td colspan="6" class="empty">No clients connected</td></tr>';
                return;
            }

            tbody.innerHTML = data.map(client => `
                <tr>
                    <td>${client.id}</td>
                    <td>${client.address}</td>
                    <td>${formatTime(client.connected_at)}</td>
                    <td>${formatTime(client.last_heartbeat)}</td>
                    <td>${client.proxies_count}</td>
                    <td>
                        <button onclick="kickClient('${client.id}')">Kick</button>
                    </td>
                </tr>
            `).join('');
        })
        .catch(err => console.error('Failed to fetch clients:', err));
}

function refreshProxies() {
    fetch('/api/proxies').then(res => res.json()).then(data => {
        const tbody = document.querySelector('#proxies-table tbody');
        if (!data || data.length === 0) { tbody.innerHTML = '<tr><td colspan="6" class="empty">No active proxies</td></tr>'; return; }
        tbody.innerHTML = data.map(p => `<tr><td>${p.Name}</td><td>${p.ClientID}</td><td>${p.Type}</td><td>${p.LocalIP}:${p.LocalPort}</td><td>${p.RemotePort}</td><td><button onclick="updateProxy('${p.Name}',${p.RemotePort})">Modify</button> <button onclick="deleteProxy('${p.Name}')">Delete</button></td></tr>`).join('');
    });
}

function updateProxy(name, currentPort) {
    const value = prompt('New remote port:', currentPort); if (!value) return;
    fetch('/api/update-proxy',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name,remote_port:parseInt(value)})})
      .then(res=>{if(!res.ok) throw new Error('update failed');return res.json();}).then(refreshProxies).catch(err=>alert(err.message));
}

function deleteProxy(name) {
    if (!confirm('Delete proxy ' + name + '?')) return;
    fetch('/api/delete-proxy', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({name})})
      .then(res => { if (!res.ok) throw new Error('delete failed'); return res.json(); })
      .then(() => { refreshProxies(); refreshClients(); }).catch(err => alert(err.message));
}

// 踢出客户端
function kickClient(clientID) {
    if (!confirm('Are you sure to kick this client?')) {
        return;
    }

    fetch('/api/client/kick', {
        method: 'POST',
        headers: {
            'Content-Type': 'application/x-www-form-urlencoded',
        },
        body: 'client_id=' + clientID
    })
    .then(res => res.json())
    .then(data => {
        alert('Client kicked');
        refreshClients();
    })
    .catch(err => {
        alert('Failed to kick client');
        console.error(err);
    });
}

// 格式化字节数
function formatBytes(bytes) {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return Math.round(bytes / Math.pow(k, i) * 100) / 100 + ' ' + sizes[i];
}

// 格式化时间
function formatTime(timeStr) {
    const date = new Date(timeStr);
    return date.toLocaleString();
}

// 加载连接token
function loadToken() {
    fetch('/api/token')
        .then(res => res.json())
        .then(data => {
            const tokenInput = document.getElementById('connection-token');
            if (tokenInput) {
                tokenInput.value = data.token;
            }
            const authTokenDisplay = document.getElementById('auth-token-display');
            if (authTokenDisplay) {
                authTokenDisplay.textContent = data.token;
            }
        })
        .catch(err => console.error('Failed to load token:', err));
}

// 复制token到剪贴板
function copyToken() {
    const tokenInput = document.getElementById('connection-token');
    tokenInput.select();
    document.execCommand('copy');
    alert('Token copied to clipboard!');
}

// 复制认证token到剪贴板
function copyAuthToken() {
    const authTokenDisplay = document.getElementById('auth-token-display');
    const token = authTokenDisplay.textContent;

    // 创建临时input元素来复制
    const tempInput = document.createElement('input');
    tempInput.value = token;
    document.body.appendChild(tempInput);
    tempInput.select();
    document.execCommand('copy');
    document.body.removeChild(tempInput);

    alert('Authentication token copied to clipboard!');
}

// 加载待审批请求
function loadPendingRequests() {
    fetch('/api/pending-requests')
        .then(res => res.json())
        .then(requests => {
            const tbody = document.getElementById('requests-body');
            if (!tbody) return;

            if (!requests || requests.length === 0) {
                tbody.innerHTML = '<tr><td colspan="5" style="text-align:center;">No pending requests</td></tr>';
                return;
            }

            tbody.innerHTML = requests.map(req =>
                '<tr>' +
                    '<td>' + escapeHtml(req.client_id) + '</td>' +
                    '<td>' + req.local_port + '</td>' +
                    '<td>' + req.proxy_type + '</td>' +
                    '<td>' + formatTime(req.request_time) + '</td>' +
                    '<td>' +
                        '<button class="btn-approve" onclick="approveRequest(\'' + req.id + '\')">Approve</button> ' +
                        '<button class="btn-reject" onclick="rejectRequest(\'' + req.id + '\')">Reject</button>' +
                    '</td>' +
                '</tr>'
            ).join('');
        })
        .catch(err => console.error('Failed to load requests:', err));
}

// 批准代理请求
function approveRequest(requestId) {
    const remotePort = prompt('Enter remote port number allowed by the server:', '');
    if (!remotePort) {
        return;
    }

    const port = parseInt(remotePort);
    if (isNaN(port) || port < 1 || port > 65535) {
        alert('Invalid port number. Please enter a port between 1-65535');
        return;
    }

    const proxyName = prompt('Enter proxy name:', 'proxy-' + port);
    if (!proxyName) {
        return;
    }

    fetch('/api/approve-request', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
            request_id: requestId,
            remote_port: port,
            proxy_name: proxyName
        })
    })
    .then(async res => {
        const text = await res.text();
        let data;
        try { data = JSON.parse(text); } catch (_) { data = { error: text.trim() }; }
        if (!res.ok) throw new Error(data.error || data.message || 'Approval failed (HTTP ' + res.status + ')');
        return data;
    })
    .then(data => {
        if (data.success) {
            alert('Request approved! Remote port: ' + data.remote_port + ', Name: ' + data.proxy_name);
            loadPendingRequests();
        } else {
            alert('Failed to approve request: ' + (data.error || 'Unknown error'));
        }
    })
    .catch(err => {
        console.error('Approve failed:', err);
        alert('Failed to approve request: ' + err.message);
    });
}

// 拒绝代理请求
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

// HTML转义
function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

// 定时刷新
setInterval(refreshStats, 1000);
setInterval(refreshClients, 3000);
setInterval(refreshProxies, 3000);
setInterval(loadPendingRequests, 3000);

// 初始加载
refreshStats();
refreshClients();
refreshProxies();
loadToken();
loadPendingRequests();
