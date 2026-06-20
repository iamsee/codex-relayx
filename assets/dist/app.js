// codex-relayx Admin UI

const API_BASE = '/admin/api';

let editingUpstreamId = null; // null = add mode, string = edit mode
let currentMappings = {};     // 当前编辑中的 model_mapping

// Tab switching
document.querySelectorAll('.tab-btn').forEach(btn => {
    btn.addEventListener('click', () => {
        document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
        document.querySelectorAll('.tab-content').forEach(c => c.classList.remove('active'));
        btn.classList.add('active');
        document.getElementById(btn.dataset.tab).classList.add('active');

        const tab = btn.dataset.tab;
        if (tab === 'dashboard') loadStats();
        else if (tab === 'upstreams') loadUpstreams();
        else if (tab === 'models') loadModels();
        else if (tab === 'logs') loadLogs();
    });
});

// ============== Stats ==============
async function loadStats() {
    const res = await fetch(`${API_BASE}/stats`);
    const data = await res.json();
    document.getElementById('stat-uptime').textContent = data.uptime;
    document.getElementById('stat-requests').textContent = data.total_requests;
    document.getElementById('stat-errors').textContent = data.errors;
    document.getElementById('stat-upstreams').textContent = data.enabled_upstreams + '/' + data.upstream_count;
}

// ============== Upstreams ==============
async function loadUpstreams() {
    const res = await fetch(`${API_BASE}/upstreams`);
    const upstreams = await res.json();
    const list = document.getElementById('upstreams-list');

    if (upstreams.length === 0) {
        list.innerHTML = '<div class="empty-state">暂无上游，点击"添加上游"开始配置</div>';
        return;
    }

    list.innerHTML = upstreams.map(u => {
        const mappingCount = Object.keys(u.model_mapping || {}).length;
        const mappingPreview = Object.entries(u.model_mapping || {}).slice(0, 3)
            .map(([k, v]) => `${k}→${v}`).join(' / ');
        const moreCount = mappingCount > 3 ? ` +${mappingCount - 3}` : '';
        return `
        <div class="upstream-card">
            <div class="card-header">
                <span class="card-title">${escapeHtml(u.name)}</span>
                <span class="badge ${u.enabled ? 'badge-success' : 'badge-warning'}">${u.enabled ? '✓ 已启用' : '✗ 已禁用'}</span>
            </div>
            <div class="card-meta">
                <span>ID: ${escapeHtml(u.id)}</span>
                <span>格式: ${u.api_format}</span>
                <span>URL: ${escapeHtml(u.base_url)}</span>
                <span>映射: ${mappingCount} 个</span>
            </div>
            ${mappingCount > 0 ? `<div style="font-size:12px;color:#909399;margin-top:6px">📐 ${escapeHtml(mappingPreview + moreCount)}</div>` : ''}
            <div class="card-actions">
                <button class="btn btn-sm" onclick="toggleUpstream('${escapeAttr(u.id)}', ${!u.enabled})">${u.enabled ? '禁用' : '启用'}</button>
                <button class="btn btn-sm" onclick="editUpstream('${escapeAttr(u.id)}')">✏️ 编辑</button>
                <button class="btn btn-sm btn-danger" onclick="deleteUpstream('${escapeAttr(u.id)}')">删除</button>
            </div>
        </div>
    `}).join('');
}

function showAddUpstream() {
    editingUpstreamId = null;
    currentMappings = {};
    document.getElementById('upstream-modal-title').textContent = '添加上游';
    const form = document.getElementById('upstream-form');
    form.reset();
    form.id.disabled = false;
    document.getElementById('mapping-list').innerHTML = '';
    document.getElementById('upstream-modal').classList.add('active');
}

async function editUpstream(id) {
    const res = await fetch(`${API_BASE}/upstreams/${id}`);
    if (!res.ok) {
        alert('未找到该上游');
        return;
    }
    const u = await res.json();
    editingUpstreamId = u.id;
    currentMappings = { ...(u.model_mapping || {}) };
    document.getElementById('upstream-modal-title').textContent = '编辑上游: ' + u.name;

    const form = document.getElementById('upstream-form');
    form.id.value = u.id;
    form.id.disabled = true; // ID 不可修改
    form.name.value = u.name || '';
    form.base_url.value = u.base_url || '';
    form.api_key.value = u.api_key || '';
    form.api_format.value = u.api_format || 'openai_chat';
    form.max_retries.value = u.max_retries ?? 3;
    form.enabled.checked = u.enabled;

    renderMappingRows();
    document.getElementById('upstream-modal').classList.add('active');
}

function renderMappingRows() {
    const container = document.getElementById('mapping-list');
    const entries = Object.entries(currentMappings);
    if (entries.length === 0) {
        container.innerHTML = '<div style="color:#909399;font-size:13px;padding:8px 0">暂无映射</div>';
        return;
    }
    container.innerHTML = entries.map(([k, v], i) => `
        <div class="mapping-row" data-key="${escapeAttr(k)}" style="display:flex;gap:8px;margin-bottom:6px;align-items:center">
            <input type="text" value="${escapeAttr(k)}" placeholder="codex 模型" style="flex:1;padding:6px 8px;border:1px solid #dcdfe6;border-radius:4px" onchange="updateMappingKey(${i}, this.value)">
            <span>→</span>
            <input type="text" value="${escapeAttr(v)}" placeholder="上游模型" style="flex:1;padding:6px 8px;border:1px solid #dcdfe6;border-radius:4px" onchange="updateMappingValue(${i}, this.value)">
            <button type="button" class="btn btn-sm btn-danger" onclick="removeMappingRow(${i})">×</button>
        </div>
    `).join('');
}

function addMappingRow() {
    const newKey = 'new_model_' + (Object.keys(currentMappings).length + 1);
    currentMappings[newKey] = '';
    renderMappingRows();
}

function updateMappingKey(index, newKey) {
    const entries = Object.entries(currentMappings);
    if (index < 0 || index >= entries.length) return;
    const [oldKey, value] = entries[index];
    if (oldKey === newKey) return;
    // 重建 map 保持顺序
    const updated = {};
    for (const [k, v] of entries) {
        if (k === oldKey) updated[newKey] = value;
        else updated[k] = v;
    }
    currentMappings = updated;
    renderMappingRows();
}

function updateMappingValue(index, newValue) {
    const entries = Object.entries(currentMappings);
    if (index < 0 || index >= entries.length) return;
    currentMappings[entries[index][0]] = newValue;
}

function removeMappingRow(index) {
    const entries = Object.entries(currentMappings);
    if (index < 0 || index >= entries.length) return;
    const [k] = entries[index];
    delete currentMappings[k];
    renderMappingRows();
}

document.getElementById('upstream-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const form = e.target;
    // 过滤空值映射
    const cleanMapping = {};
    for (const [k, v] of Object.entries(currentMappings)) {
        if (k.trim() && v.trim()) cleanMapping[k.trim()] = v.trim();
    }
    const data = {
        id: form.id.value,
        name: form.name.value,
        base_url: form.base_url.value,
        api_key: form.api_key.value,
        api_format: form.api_format.value,
        enabled: form.enabled.checked,
        model_mapping: cleanMapping,
        max_retries: parseInt(form.max_retries.value, 10) || 3,
    };

    const isEdit = editingUpstreamId !== null;
    const url = isEdit
        ? `${API_BASE}/upstreams/${editingUpstreamId}`
        : `${API_BASE}/upstreams`;
    const method = isEdit ? 'PUT' : 'POST';

    const res = await fetch(url, {
        method,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(data),
    });

    if (res.ok) {
        closeModal('upstream-modal');
        form.reset();
        form.id.disabled = false;
        loadUpstreams();
    } else {
        let err = '操作失败';
        try { err = (await res.json()).error || err; } catch (_) {}
        alert((isEdit ? '更新' : '添加') + '失败: ' + err);
    }
});

async function toggleUpstream(id, enabled) {
    const res = await fetch(`${API_BASE}/upstreams/${id}`);
    if (!res.ok) return alert('未找到该上游');
    const upstream = await res.json();
    upstream.enabled = enabled;

    const putRes = await fetch(`${API_BASE}/upstreams/${id}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(upstream),
    });
    if (putRes.ok) loadUpstreams();
    else alert('更新失败');
}

async function deleteUpstream(id) {
    if (!confirm('确定要删除这个上游吗？')) return;
    await fetch(`${API_BASE}/upstreams/${id}`, { method: 'DELETE' });
    loadUpstreams();
}

// ============== Models (global view) ==============
async function loadModels() {
    const res = await fetch(`${API_BASE}/models`);
    const models = await res.json();
    const list = document.getElementById('models-list');

    if (models.length === 0) {
        list.innerHTML = '<div class="empty-state">暂无模型映射。可在"上游"标签页的编辑弹窗中管理每个上游的模型映射；此处用于添加全局映射。</div>';
        return;
    }

    list.innerHTML = models.map(m => `
        <div class="model-card">
            <div class="card-header">
                <span class="card-title">${escapeHtml(m.codex_model)} → ${escapeHtml(m.upstream_model)}</span>
                <span class="badge ${m.enabled ? 'badge-success' : 'badge-warning'}">${escapeHtml(m.upstream_id)}</span>
            </div>
            <div class="card-meta">
                <span>作用域: ${m.upstream_id === 'global' ? '全局（应用于所有上游）' : '上游级'}</span>
            </div>
            <div class="card-actions">
                ${m.upstream_id !== 'global' ? `<button class="btn btn-sm" onclick="goEditUpstream('${escapeAttr(m.upstream_id)}')">在上游中编辑</button>` : ''}
                <button class="btn btn-sm btn-danger" onclick="deleteModel('${escapeAttr(m.codex_model)}', '${escapeAttr(m.upstream_id)}')">删除</button>
            </div>
        </div>
    `).join('');
}

function goEditUpstream(id) {
    // 切到上游标签页并打开编辑弹窗
    document.querySelector('[data-tab="upstreams"]').click();
    setTimeout(() => editUpstream(id), 100);
}

function showAddModel() {
    document.getElementById('model-modal').classList.add('active');
}

document.getElementById('model-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const form = e.target;
    const data = {
        codex_model: form.codex_model.value,
        upstream_model: form.upstream_model.value,
        upstream_id: form.upstream_id.value,
    };

    const res = await fetch(`${API_BASE}/models`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(data),
    });

    if (res.ok) {
        closeModal('model-modal');
        form.reset();
        loadModels();
    } else {
        let err = '添加失败';
        try { err = (await res.json()).error || err; } catch (_) {}
        alert(err);
    }
});

async function deleteModel(name, upstreamId) {
    if (!confirm(`确定要删除映射 ${name} 吗？\n（如果来自某个上游，将从该上游的 model_mapping 中删除）`)) return;

    // 后端 deleteModel 用 codex_model 作为 key，会同时从 global 和所有 upstream 中删除
    // 这里需要更精细：先尝试从指定 upstream 删除
    if (upstreamId && upstreamId !== 'global') {
        const res = await fetch(`${API_BASE}/upstreams/${upstreamId}`);
        if (res.ok) {
            const u = await res.json();
            if (u.model_mapping && u.model_mapping[name] !== undefined) {
                delete u.model_mapping[name];
                await fetch(`${API_BASE}/upstreams/${upstreamId}`, {
                    method: 'PUT',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(u),
                });
                loadModels();
                return;
            }
        }
    }
    // 否则走全局删除
    await fetch(`${API_BASE}/models/${encodeURIComponent(name)}`, { method: 'DELETE' });
    loadModels();
}

// ============== Logs ==============
async function loadLogs() {
    const res = await fetch(`${API_BASE}/logs?limit=100`);
    const logs = await res.json();
    const list = document.getElementById('logs-list');

    if (logs.length === 0) {
        list.innerHTML = '<div class="empty-state">暂无日志</div>';
        return;
    }

    list.innerHTML = logs.reverse().map(log => `
        <div class="log-entry ${log.status_code >= 400 ? 'error' : ''}">
            <span class="log-time">${new Date(log.timestamp).toLocaleString()}</span>
            <span class="log-method">${log.method}</span>
            <span class="log-status ${log.status_code >= 400 ? 'error' : ''}">${log.status_code}</span>
            ${log.path} | ${log.model} → ${log.upstream_model || '-'} | ${log.latency_ms}ms
            ${log.error ? `<br><span style="color:#f56c6c">Error: ${log.error}</span>` : ''}
        </div>
    `).join('');
}

// ============== Utilities ==============
function closeModal(id) {
    document.getElementById(id).classList.remove('active');
    editingUpstreamId = null;
}

function escapeHtml(s) {
    if (s === null || s === undefined) return '';
    return String(s)
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#39;');
}

function escapeAttr(s) {
    return escapeHtml(s);
}

// Initial load
loadStats();
setInterval(loadStats, 5000);
