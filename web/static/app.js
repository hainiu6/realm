document.addEventListener('DOMContentLoaded', () => {
    const outputDiv = document.getElementById('output');
    const openAddRuleButton = document.getElementById('openAddRuleButton');
    const openBatchRulesButton = document.getElementById('openBatchRulesButton');
    const startButton = document.getElementById('startButton');
    const stopButton = document.getElementById('stopButton');
    const restartButton = document.getElementById('restartButton');
    const addRuleButton = document.getElementById('addRuleButton');
    const cancelEditButton = document.getElementById('cancelEditButton');
    const ruleFormTitle = document.getElementById('ruleFormTitle');
    const addBatchRulesButton = document.getElementById('addBatchRulesButton');
    const logoutButton = document.getElementById('logoutButton');
    const localPortInput = document.getElementById('localPort');
    const remoteIPInput = document.getElementById('remoteIP');
    const remotePortInput = document.getElementById('remotePort');
    const extraRemotesInput = document.getElementById('extraRemotes');
    const balanceStrategyInput = document.getElementById('balanceStrategy');
    const balanceWeightsInput = document.getElementById('balanceWeights');
    const rulesInput = document.getElementById('rulesInput');
    const ruleModal = document.getElementById('ruleModal');
    const batchRulesModal = document.getElementById('batchRulesModal');
    const selectAllRules = document.getElementById('selectAllRules');
    const deleteSelectedButton = document.getElementById('deleteSelectedButton');
    const copySelectedButton = document.getElementById('copySelectedButton');
    const clearTrafficButton = document.getElementById('clearTrafficButton');
    const trafficWarning = document.getElementById('trafficWarning');
    const selectedCount = document.getElementById('selectedCount');
    const closeRuleModalButton = document.getElementById('closeRuleModalButton');
    const closeBatchModalButton = document.getElementById('closeBatchModalButton');
    const cancelBatchButton = document.getElementById('cancelBatchButton');
    const ruleModalMessage = document.getElementById('ruleModalMessage');
    const batchModalMessage = document.getElementById('batchModalMessage');

    let allRules = [];
    let currentPage = 1;
    let pageSize = 1000;
    let totalRules = 0;
    let editingListen = null;
    let trafficAvailable = false;
    const selectedRules = new Map();

    const pageSizeSelect = document.getElementById('pageSizeSelect');
    const uploadSpeed = document.getElementById('uploadSpeed');
    const downloadSpeed = document.getElementById('downloadSpeed');
    const speedSummary = document.getElementById('speedSummary');
    let speedRequestPending = false;

    async function updateTrafficSpeed() {
        if (speedRequestPending || document.hidden) return;
        speedRequestPending = true;
        const controller = new AbortController();
        const timeout = setTimeout(() => controller.abort(), 5000);
        try {
            const response = await fetch('/traffic_speed', { cache: 'no-store', signal: controller.signal });
            if (!response.ok) throw new Error('读取速度失败');
            const data = await response.json();
            if (!data.available) throw new Error(data.warning || '速度统计暂不可用');
            uploadSpeed.textContent = formatSpeed(data.upload_bytes_per_second);
            downloadSpeed.textContent = formatSpeed(data.download_bytes_per_second);
            speedSummary.title = '所有 Realm 规则的合计速度，按 bit/s 显示（字节速度 × 8），每 3 秒更新';
            speedSummary.classList.remove('unavailable');
        } catch (error) {
            uploadSpeed.textContent = '—';
            downloadSpeed.textContent = '—';
            speedSummary.title = error.message;
            speedSummary.classList.add('unavailable');
        } finally {
            clearTimeout(timeout);
            speedRequestPending = false;
        }
    }

    async function updateServerIP() {
        const element = document.getElementById('serverIP');
        try {
            const response = await fetch('/server_info', { cache: 'no-store' });
            if (!response.ok) {
                throw new Error('读取本机 IP 失败');
            }
            const data = await response.json();
            const ips = Array.isArray(data.ips) ? data.ips : [];
            element.textContent = ips.length ? ips.join(' · ') : '暂无可用地址';
        } catch (error) {
            element.textContent = '获取失败';
            console.error('本机 IP 获取失败:', error);
        }
    }

    async function updateServiceStatus() {
        try {
            const response = await fetch(`/check_status?_=${Date.now()}`);
            if (!response.ok) {
                throw new Error('检查状态失败：' + response.statusText);
            }
            const data = await response.json();
            const statusElement = document.getElementById('serviceStatus');
            
            if (data.status === "启用") {
                statusElement.textContent = "运行中";
                statusElement.className = 'status-tag running';
            } else {
                statusElement.textContent = "已停止";
                statusElement.className = 'status-tag stopped';
            }
        } catch (error) {
            console.error('状态检查失败:', error);
            const statusElement = document.getElementById('serviceStatus');
            statusElement.textContent = "未知";
            statusElement.className = 'status-tag stopped';
        }
    }

    async function fetchForwardingRules(targetPage = currentPage) {
        try {
            const response = await fetch(`/get_rules?page=${targetPage}&size=${pageSize}&_=${Date.now()}`, {
                method: 'GET',
                headers: {
                    'Cache-Control': 'no-cache',
                    'Pragma': 'no-cache'
                },
            });

            if (!response.ok) {
                throw new Error('获取规则失败：' + response.statusText);
            }

            const data = await response.json();
            if (!Array.isArray(data.rules)) {
                data.rules = [];
            }

            totalRules = data.total;
            trafficAvailable = data.traffic_available !== false;
            trafficWarning.textContent = data.traffic_warning || '';
            document.getElementById('dailyTrafficPeriod').textContent = data.traffic_today
                ? `按服务器时间（${data.traffic_timezone}）统计：今日 ${data.traffic_today}，昨日 ${data.traffic_yesterday}`
                : '每日流量从升级后开始统计，按服务器时间划分。';
            const totalPages = Math.max(1, Math.ceil(totalRules / pageSize));
            currentPage = Math.min(Math.max(1, targetPage), totalPages);

            allRules = data.rules.map(rule => {
                const listen = rule.Listen || rule.listen;
                const remote = rule.Remote || rule.remote;
                const extraRemotes = rule.ExtraRemotes || rule.extra_remotes || [];
                const balance = rule.Balance || rule.balance || '';
                return {
                    listen, remote, extraRemotes, balance,
                    disabled: Boolean(rule.disabled),
                    uploadBytes: rule.upload_bytes || 0,
                    downloadBytes: rule.download_bytes || 0,
                    todayUploadBytes: rule.today_upload_bytes || 0,
                    todayDownloadBytes: rule.today_download_bytes || 0,
                    yesterdayUploadBytes: rule.yesterday_upload_bytes || 0,
                    yesterdayDownloadBytes: rule.yesterday_download_bytes || 0,
                    dailyWarning: rule.daily_warning || ''
                };
            });
            allRules.forEach(rule => {
                if (selectedRules.has(rule.listen)) {
                    selectedRules.set(rule.listen, rule);
                }
            });

            renderForwardingRules();

            return allRules;
        } catch (error) {
            console.error('获取规则失败:', error);
            outputDiv.textContent = `获取转发规则失败: ${error.message}`;
            return [];
        }
    }

    async function refreshRulesAfterChange() {
        const totalPagesAfterChange = Math.max(1, Math.ceil(totalRules / pageSize));
        await fetchForwardingRules(totalPagesAfterChange);
    }

    function splitHostPort(value) {
        if (!value) {
            return null;
        }

        if (value.startsWith('[')) {
            const closingBracketIndex = value.indexOf(']');
            if (closingBracketIndex === -1 || value[closingBracketIndex + 1] !== ':') {
                return null;
            }

            const host = value.slice(1, closingBracketIndex);
            const port = value.slice(closingBracketIndex + 2);
            if (!host || !port) {
                return null;
            }

            return { host, port };
        }

        const colonIndex = value.lastIndexOf(':');
        if (colonIndex <= 0 || colonIndex === value.length - 1) {
            return null;
        }

        if (value.includes(':', colonIndex + 1) || value.includes(':') && value.indexOf(':') !== colonIndex) {
            return null;
        }

        return {
            host: value.slice(0, colonIndex),
            port: value.slice(colonIndex + 1)
        };
    }

    function renderForwardingRules() {
        const table = document.getElementById('forwardingTable');
        const tbody = document.querySelector('#forwardingTable tbody');
        tbody.innerHTML = '';
        table.classList.toggle('empty-table', allRules.length === 0);

        if (allRules.length === 0) {
            const row = document.createElement('tr');
            const cell = document.createElement('td');
            cell.colSpan = 12;
            cell.className = 'empty-state';
            cell.textContent = '暂无转发规则';
            row.appendChild(cell);
            tbody.appendChild(row);
        }

        allRules.forEach((rule, index) => {
            const listen = rule.listen || '';
            const remote = rule.remote || '';
            const extraRemotes = Array.isArray(rule.extraRemotes) ? rule.extraRemotes : [];
            const balance = rule.balance || '';

            if (!listen || !remote) return;

            const listenParts = splitHostPort(listen);
            const localPort = listenParts?.port || listen;

            const row = document.createElement('tr');
            row.classList.toggle('paused-row', rule.disabled);
            const selectCell = document.createElement('td');
            selectCell.className = 'checkbox-cell';
            const checkbox = document.createElement('input');
            checkbox.type = 'checkbox';
            checkbox.className = 'rule-select';
            checkbox.checked = selectedRules.has(listen);
            checkbox.setAttribute('aria-label', `选择端口 ${localPort}`);
            row.classList.toggle('selected-row', checkbox.checked);
            checkbox.addEventListener('change', () => {
                if (checkbox.checked) {
                    selectedRules.set(listen, rule);
                } else {
                    selectedRules.delete(listen);
                }
                row.classList.toggle('selected-row', checkbox.checked);
                updateSelectionControls();
            });
            selectCell.appendChild(checkbox);
            row.appendChild(selectCell);

            const values = [
                (currentPage - 1) * pageSize + index + 1,
                localPort,
                [remote, ...extraRemotes].join('\n'),
                formatBalance(balance, extraRemotes.length + 1),
                formatTraffic(rule.uploadBytes),
                formatTraffic(rule.downloadBytes),
                formatTraffic(rule.todayUploadBytes),
                formatTraffic(rule.todayDownloadBytes),
                formatTraffic(rule.yesterdayUploadBytes),
                formatTraffic(rule.yesterdayDownloadBytes)
            ];
            const labels = ['序号', '中转端口', '远端节点', '负载均衡', '已用上传', '已用下载', '今日上传', '今日下载', '昨日上传', '昨日下载'];
            values.forEach((value, cellIndex) => {
                const cell = document.createElement('td');
                cell.textContent = value;
                cell.dataset.label = labels[cellIndex];
                if (cellIndex === 2) {
                    cell.className = 'remote-list';
                }
                if (cellIndex === 1) {
                    const portLabel = document.createElement('span');
                    portLabel.className = 'port-label';
                    const state = document.createElement('span');
                    state.className = `rule-status-dot ${rule.disabled ? 'paused' : 'enabled'}`;
                    state.title = rule.disabled ? '已暂停' : '已启用';
                    state.setAttribute('role', 'img');
                    state.setAttribute('aria-label', state.title);
                    portLabel.append(state, document.createTextNode(value));
                    cell.replaceChildren(portLabel);
                }
                if (cellIndex >= 4) {
                    cell.className = 'traffic-value';
                    if (!trafficAvailable) cell.title = '统计暂不可用，显示上次保存值';
                    if (cellIndex >= 6 && rule.dailyWarning) {
                        cell.classList.add('daily-incomplete');
                        cell.title = [cell.title, rule.dailyWarning].filter(Boolean).join('；');
                    }
                }
                row.appendChild(cell);
            });

            const actionCell = document.createElement('td');
            actionCell.className = 'table-actions';
            const toggleButton = document.createElement('button');
            toggleButton.className = rule.disabled ? 'enable-btn' : 'pause-btn';
            toggleButton.textContent = rule.disabled ? '启用' : '暂停';
            toggleButton.addEventListener('click', () => toggleRule(rule, toggleButton));
            actionCell.appendChild(toggleButton);
            const editButton = document.createElement('button');
            editButton.className = 'edit-btn';
            editButton.textContent = '编辑';
            editButton.addEventListener('click', () => beginEdit(rule));
            actionCell.appendChild(editButton);

            const deleteButton = document.createElement('button');
            deleteButton.className = 'delete-btn';
            deleteButton.dataset.listen = listen;
            deleteButton.textContent = '删除';
            actionCell.appendChild(deleteButton);
            row.appendChild(actionCell);
            tbody.appendChild(row);
        });

        document.querySelectorAll('.delete-btn').forEach(button => {
            button.addEventListener('click', function() {
                deleteRule(this.getAttribute('data-listen'));
            });
        });

        updatePaginationInfo();
        updateSelectionControls();
    }

    function updateSelectionControls() {
        const visibleListens = allRules.map(rule => rule.listen).filter(Boolean);
        const selectedVisibleCount = visibleListens.filter(listen => selectedRules.has(listen)).length;

        selectAllRules.checked = visibleListens.length > 0 && selectedVisibleCount === visibleListens.length;
        selectAllRules.indeterminate = selectedVisibleCount > 0 && selectedVisibleCount < visibleListens.length;
        selectAllRules.disabled = visibleListens.length === 0;
        selectedCount.textContent = `已选 ${selectedRules.size} 条`;
        deleteSelectedButton.disabled = selectedRules.size === 0;
        copySelectedButton.disabled = selectedRules.size === 0;
        clearTrafficButton.disabled = selectedRules.size === 0 || !trafficAvailable;
    }

    function formatTraffic(bytes) {
        const value = Number(bytes) || 0;
        if (value === 0) return '0 B';
        const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
        const index = Math.max(0, Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1));
        return `${(value / Math.pow(1024, index)).toFixed(index === 0 ? 0 : 2)} ${units[index]}`;
    }

    function formatSpeed(bytesPerSecond) {
        const bits = Number(bytesPerSecond) * 8;
        if (!Number.isFinite(bits) || bits <= 0) return '0 bit/s';
        const units = ['bit/s', 'Kbit/s', 'Mbit/s', 'Gbit/s', 'Tbit/s', 'Pbit/s'];
        const index = Math.max(0, Math.min(Math.floor(Math.log(bits) / Math.log(1000)), units.length - 1));
        return `${(bits / Math.pow(1000, index)).toFixed(index === 0 ? 0 : 2)} ${units[index]}`;
    }

    async function toggleRule(rule, button) {
        button.disabled = true;
        try {
            const response = await fetch('/toggle_rule', {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ listen: rule.listen, disabled: !rule.disabled })
            });
            const result = await response.json();
            if (!response.ok) throw new Error(result.error || '切换规则状态失败');
            outputDiv.textContent = `规则已${rule.disabled ? '启用' : '暂停'}，Realm 已重启`;
        } catch (error) {
            outputDiv.textContent = error.message;
        } finally {
            await fetchForwardingRules(currentPage);
            await updateServiceStatus();
            button.disabled = false;
        }
    }

    async function clearSelectedTraffic() {
        const listens = Array.from(selectedRules.keys());
        if (!listens.length || !window.confirm(`确定清空选中的 ${listens.length} 条规则的累计上传和下载流量吗？今日、昨日流量记录会保留。`)) return;
        clearTrafficButton.disabled = true;
        try {
            const response = await fetch('/reset_traffic', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ listens })
            });
            const result = await response.json();
            if (!response.ok) throw new Error(result.error || '清空流量失败');
            await fetchForwardingRules(currentPage);
            outputDiv.textContent = `已清空 ${result.cleared} 条规则的累计流量，今日/昨日记录保留，转发服务未中断`;
        } catch (error) {
            outputDiv.textContent = error.message;
        } finally {
            updateSelectionControls();
        }
    }

    function exportRule(rule) {
        const port = splitHostPort(rule.listen)?.port;
        const extraRemotes = Array.isArray(rule.extraRemotes) ? rule.extraRemotes : [];
        if (port && rule.listen === `[::]:${port}` && extraRemotes.length === 0 && !rule.balance && !rule.disabled) {
            return `${port}:${rule.remote}`;
        }
        return JSON.stringify({
            listen: rule.listen,
            remote: rule.remote,
            extra_remotes: extraRemotes,
            balance: rule.balance || '',
            ...(rule.disabled ? { disabled: true } : {})
        });
    }

    async function writeClipboard(text) {
        if (navigator.clipboard?.writeText) {
            try {
                await navigator.clipboard.writeText(text);
                return;
            } catch (_) {
                // HTTP 面板或权限受限时尝试浏览器的传统复制方式。
            }
        }
        const previousFocus = document.activeElement;
        const textarea = document.createElement('textarea');
        textarea.value = text;
        textarea.readOnly = true;
        textarea.style.cssText = 'position:fixed;left:-9999px;top:0;';
        document.body.appendChild(textarea);
        try {
            textarea.select();
            textarea.setSelectionRange(0, text.length);
            if (!document.execCommand('copy')) {
                throw new Error('浏览器不允许自动复制');
            }
        } finally {
            textarea.remove();
            previousFocus?.focus();
        }
    }

    async function copySelectedRules() {
        if (selectedRules.size === 0) return;
        const text = Array.from(selectedRules.values()).map(exportRule).join('\n');
        const count = selectedRules.size;
        copySelectedButton.disabled = true;
        try {
            await writeClipboard(text);
            outputDiv.textContent = `已复制 ${count} 条规则，可粘贴到批量添加中导入`;
        } catch (error) {
            rulesInput.value = text;
            showModalMessage(batchModalMessage, '浏览器不允许自动复制，内容已选中，请手动复制后粘贴到目标面板的批量添加中。');
            openModal(batchRulesModal);
            rulesInput.focus();
            rulesInput.select();
        } finally {
            updateSelectionControls();
        }
    }

    function parseBatchRule(line) {
        if (line.startsWith('{')) {
            const value = JSON.parse(line);
            if (typeof value.listen !== 'string' || typeof value.remote !== 'string' ||
                (value.extra_remotes !== undefined && (!Array.isArray(value.extra_remotes) || value.extra_remotes.some(remote => typeof remote !== 'string'))) ||
                (value.balance !== undefined && typeof value.balance !== 'string') ||
                (value.disabled !== undefined && typeof value.disabled !== 'boolean')) {
                throw new Error('复制规则的字段格式不正确');
            }
            return {
                listen: value.listen,
                remote: value.remote,
                extra_remotes: value.extra_remotes || [],
                balance: value.balance || '',
                ...(value.disabled ? { disabled: true } : {})
            };
        }
        const match = line.match(/^(\d+):(\[.*?\]:\d+|\S+)$/);
        if (!match) {
            throw new Error('请填写“中转端口:远端地址:目标端口”或粘贴复制的规则');
        }
        return { listen: `[::]:${match[1]}`, remote: match[2] };
    }

    function formatBalance(balance, backendCount) {
        if (!balance) {
            return backendCount > 1 ? '未设置' : '单节点';
        }

        const colonIndex = balance.indexOf(':');
        const strategy = colonIndex === -1 ? balance : balance.slice(0, colonIndex).trim();
        const weights = colonIndex === -1 ? '' : balance.slice(colonIndex + 1).trim();
        const strategyLabel = strategy === 'roundrobin' ? '轮询' : strategy === 'iphash' ? '来源 IP 固定' : strategy;
        return weights ? `${strategyLabel}\n权重 ${weights}` : strategyLabel;
    }

    function formatHostPort(host, port) {
        const trimmedHost = host.trim();
        if (trimmedHost.startsWith('[') && trimmedHost.endsWith(']')) {
            return `${trimmedHost}:${port}`;
        }
        if (trimmedHost.includes(':')) {
            return `[${trimmedHost}]:${port}`;
        }
        return `${trimmedHost}:${port}`;
    }

    function formatListenAddress(port) {
        if (editingListen) {
            const originalListen = splitHostPort(editingListen);
            if (originalListen) {
                return formatHostPort(originalListen.host, port);
            }
        }
        return `[::]:${port}`;
    }

    function updateBalanceFields() {
        const hasExtraRemotes = extraRemotesInput.value.trim().length > 0;
        balanceStrategyInput.disabled = !hasExtraRemotes;
        balanceWeightsInput.disabled = !hasExtraRemotes;
    }

    function showModalMessage(element, message = '') {
        element.textContent = message;
        element.hidden = !message;
    }

    function openModal(modal, focusElement) {
        modal.hidden = false;
        document.body.classList.add('modal-open');
        if (focusElement) {
            requestAnimationFrame(() => focusElement.focus());
        }
    }

    function closeModal(modal) {
        modal.hidden = true;
        if (ruleModal.hidden && batchRulesModal.hidden) {
            document.body.classList.remove('modal-open');
        }
    }

    function closeRuleModal() {
        closeModal(ruleModal);
        resetRuleForm();
    }

    function closeBatchModal() {
        closeModal(batchRulesModal);
        showModalMessage(batchModalMessage);
    }

    function beginEdit(rule) {
        const listenParts = splitHostPort(rule.listen);
        const remoteParts = splitHostPort(rule.remote);
        if (!listenParts || !remoteParts) {
            outputDiv.textContent = '该规则地址格式无法在表单中编辑';
            return;
        }

        editingListen = rule.listen;
        localPortInput.value = listenParts.port;
        remoteIPInput.value = remoteParts.host;
        remotePortInput.value = remoteParts.port;
        extraRemotesInput.value = (rule.extraRemotes || []).join('\n');

        const balance = rule.balance || '';
        const colonIndex = balance.indexOf(':');
        if (colonIndex !== -1) {
            const strategy = balance.slice(0, colonIndex).trim();
            if (strategy === 'roundrobin' || strategy === 'iphash') {
                balanceStrategyInput.value = strategy;
            }
            balanceWeightsInput.value = balance.slice(colonIndex + 1).trim();
        } else {
            balanceStrategyInput.value = 'roundrobin';
            balanceWeightsInput.value = '';
        }

        updateBalanceFields();
        ruleFormTitle.textContent = '编辑转发规则';
        addRuleButton.textContent = '保存修改';
        showModalMessage(ruleModalMessage);
        openModal(ruleModal, localPortInput);
    }

    function resetRuleForm() {
        editingListen = null;
        localPortInput.value = '';
        remoteIPInput.value = '';
        remotePortInput.value = '';
        extraRemotesInput.value = '';
        balanceWeightsInput.value = '';
        balanceStrategyInput.value = 'roundrobin';
        ruleFormTitle.textContent = '添加转发规则';
        addRuleButton.textContent = '添加规则';
        showModalMessage(ruleModalMessage);
        updateBalanceFields();
    }

    function updatePaginationInfo() {
        const pageInfo = document.getElementById('pageInfo');
        const totalPages = Math.ceil(totalRules / pageSize);
        pageInfo.textContent = `第 ${currentPage} / ${totalPages === 0 ? 1 : totalPages} 页`;

        document.getElementById('prevPage').disabled = (currentPage <= 1);
        document.getElementById('nextPage').disabled = (currentPage >= totalPages || totalPages === 0);
    }

    function goToPrevPage() {
        if (currentPage > 1) {
            currentPage--;
            fetchForwardingRules();
        }
    }

    function goToNextPage() {
        const totalPages = Math.ceil(totalRules / pageSize);
        if (currentPage < totalPages) {
            currentPage++;
            fetchForwardingRules();
        }
    }

    async function deleteRule(listenAddress) {
        try {
            const response = await fetch(`/delete_rule?listen=${encodeURIComponent(listenAddress)}`, {
                method: 'DELETE'
            });

            if (!response.ok) {
                throw new Error('删除规则失败：' + response.statusText);
            }

            selectedRules.delete(listenAddress);
            if (editingListen === listenAddress) {
                resetRuleForm();
            }
            totalRules = Math.max(0, totalRules - 1);
            const targetPage = Math.min(currentPage, Math.max(1, Math.ceil(totalRules / pageSize)));
            await fetchForwardingRules(targetPage);

            const restartResponse = await fetch('/restart_service', {
                method: 'POST'
            });
            if (!restartResponse.ok) {
                throw new Error('规则已删除，但重启服务失败：' + restartResponse.statusText);
            }

            await updateServiceStatus();
            outputDiv.textContent = '规则已删除，服务已重启';
        } catch (error) {
            console.error('删除失败:', error);
            outputDiv.textContent = error.message;
        }
    }

    async function deleteSelectedRules() {
        const listens = Array.from(selectedRules.keys());
        if (listens.length === 0) {
            return;
        }

        if (!window.confirm(`确定删除选中的 ${listens.length} 条规则吗？`)) {
            return;
        }

        deleteSelectedButton.disabled = true;
        try {
            const response = await fetch('/delete_rules', {
                method: 'DELETE',
                headers: {
                    'Content-Type': 'application/json'
                },
                body: JSON.stringify({ listens })
            });

            if (!response.ok) {
                let detail = response.statusText;
                try {
                    const errorData = await response.json();
                    detail = errorData.error || detail;
                } catch (_) {
                    // 保留 HTTP 状态文本。
                }
                throw new Error('批量删除失败：' + detail);
            }

            const result = await response.json();
            const deleted = Number(result.deleted) || listens.length;
            selectedRules.clear();
            totalRules = Math.max(0, totalRules - deleted);
            const targetPage = Math.min(currentPage, Math.max(1, Math.ceil(totalRules / pageSize)));
            await fetchForwardingRules(targetPage);

            const restartResponse = await fetch('/restart_service', { method: 'POST' });
            if (!restartResponse.ok) {
                throw new Error('规则已删除，但重启服务失败：' + restartResponse.statusText);
            }

            await updateServiceStatus();
            outputDiv.textContent = `已删除 ${deleted} 条规则，服务已重启`;
        } catch (error) {
            console.error('批量删除失败:', error);
            outputDiv.textContent = error.message;
            updateSelectionControls();
        }
    }

    async function addRule() {
        const localPort = localPortInput.value.trim();
        const remoteIP = remoteIPInput.value.trim();
        const remotePort = remotePortInput.value.trim();
        const extraRemotes = extraRemotesInput.value
            .split('\n')
            .map(value => value.trim())
            .filter(Boolean);

        if (!localPort || !remoteIP || !remotePort) {
            showModalMessage(ruleModalMessage, '请填写所有必填字段');
            return;
        }

        try {
            const portIsUsed = allRules.some(rule => {
                const rulePort = splitHostPort(rule.listen)?.port;
                return rule.listen !== editingListen && rulePort === localPort;
            });
            if (portIsUsed) {
                showModalMessage(ruleModalMessage, `端口 ${localPort} 已被占用`);
                return;
            }

            const backendCount = 1 + extraRemotes.length;
            let balance = '';
            if (extraRemotes.length > 0) {
                let weights;
                if (balanceWeightsInput.value.trim()) {
                    weights = balanceWeightsInput.value.split(',').map(value => value.trim());
                    if (weights.length !== backendCount || weights.some(value => !/^\d+$/.test(value) || Number(value) < 1)) {
                        showModalMessage(ruleModalMessage, `权重必须填写 ${backendCount} 个大于 0 的整数`);
                        return;
                    }
                } else {
                    weights = Array(backendCount).fill('1');
                }
                balance = `${balanceStrategyInput.value}: ${weights.join(', ')}`;
            }

            const isEditing = editingListen !== null;
            const updatedRule = {
                listen: formatListenAddress(localPort),
                remote: formatHostPort(remoteIP, remotePort),
                extraRemotes,
                balance,
                disabled: selectedRules.get(editingListen)?.disabled || allRules.find(rule => rule.listen === editingListen)?.disabled || false
            };
            const requestURL = isEditing
                ? `/update_rule?listen=${encodeURIComponent(editingListen)}`
                : '/add_rule';
            const response = await fetch(requestURL, {
                method: isEditing ? 'PUT' : 'POST',
                headers: {
                    'Content-Type': 'application/json'
                },
                body: JSON.stringify({
                    listen: formatListenAddress(localPort),
                    remote: formatHostPort(remoteIP, remotePort),
                    extra_remotes: extraRemotes,
                    balance
                })
            });

            if (!response.ok) {
                let detail = response.statusText;
                try {
                    const errorData = await response.json();
                    detail = errorData.error || detail;
                } catch (_) {
                    // 保留 HTTP 状态文本。
                }
                throw new Error(`${isEditing ? '修改' : '添加'}规则失败：${detail}`);
            }

            if (isEditing && selectedRules.has(editingListen)) {
                selectedRules.delete(editingListen);
                selectedRules.set(updatedRule.listen, updatedRule);
                updateSelectionControls();
            }

            const restartResponse = await fetch('/restart_service', {
                method: 'POST'
            });
            if (!restartResponse.ok) {
                throw new Error('重启服务失败：' + restartResponse.statusText);
            }

            outputDiv.textContent = `规则${isEditing ? '修改' : '添加'}成功，服务已重启`;
            closeRuleModal();
            if (isEditing) {
                await fetchForwardingRules(currentPage);
            } else {
                totalRules += 1;
                await refreshRulesAfterChange();
            }
            await updateServiceStatus();
        } catch (error) {
            console.error('添加失败:', error);
            showModalMessage(ruleModalMessage, error.message);
        }
    }

    async function addBatchRules() {
        const rules = rulesInput.value.split('\n').map(line => line.trim()).filter(Boolean);
        if (rules.length === 0) {
            showModalMessage(batchModalMessage, '请输入要添加的规则');
            return;
        }

        const usedListens = new Set(allRules.map(rule => rule.listen));
        const failedRules = [];
        const failedLines = [];
        let successCount = 0;
        addBatchRulesButton.disabled = true;

        for (const [index, line] of rules.entries()) {
            try {
                const rule = parseBatchRule(line);
                if (usedListens.has(rule.listen)) {
                    throw new Error(`监听地址 ${rule.listen} 已存在`);
                }
                const response = await fetch('/add_rule', {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json'
                    },
                    body: JSON.stringify(rule)
                });

                if (!response.ok) {
                    let detail = response.statusText;
                    try {
                        const data = await response.json();
                        detail = data.error || detail;
                    } catch (_) {
                        // 保留 HTTP 状态文本。
                    }
                    throw new Error(detail || '添加失败');
                }

                usedListens.add(rule.listen);
                successCount++;
            } catch (error) {
                failedLines.push(line);
                failedRules.push(`第 ${index + 1} 行：${error.message}`);
            }
        }

        if (successCount > 0) {
            try {
                const restartResponse = await fetch('/restart_service', {
                    method: 'POST'
                });
                if (!restartResponse.ok) {
                    throw new Error('重启服务失败');
                }
            } catch (error) {
                failedRules.push('规则已添加，但服务重启失败，请手动重启服务');
            }
        }

        if (successCount > 0) {
            totalRules += successCount;
            await refreshRulesAfterChange();
            await updateServiceStatus();
        }
        addBatchRulesButton.disabled = false;
        rulesInput.value = failedLines.join('\n');

        if (failedRules.length > 0) {
            showModalMessage(batchModalMessage, `已添加 ${successCount} 条，失败 ${failedLines.length} 条。\n${failedRules.join('\n')}`);
        } else {
            outputDiv.textContent = `已添加 ${successCount} 条规则，服务已重启`;
            closeBatchModal();
        }
    }

    startButton.addEventListener('click', async () => {
        try {
            const response = await fetch('/start_service', {
                method: 'POST'
            });
            if (!response.ok) {
                throw new Error('启动服务失败：' + response.statusText);
            }
            outputDiv.textContent = '服务启动成功';
            await updateServiceStatus();
        } catch (error) {
            console.error('启动失败:', error);
            outputDiv.textContent = error.message;
        }
    });

    stopButton.addEventListener('click', async () => {
        try {
            const response = await fetch('/stop_service', {
                method: 'POST'
            });
            if (!response.ok) {
                throw new Error('停止服务失败：' + response.statusText);
            }
            outputDiv.textContent = '服务停止成功';
            await updateServiceStatus();
        } catch (error) {
            console.error('停止失败:', error);
            outputDiv.textContent = error.message;
        }
    });

    restartButton.addEventListener('click', async () => {
        try {
            const response = await fetch('/restart_service', {
                method: 'POST'
            });
            if (!response.ok) {
                throw new Error('重启服务失败：' + response.statusText);
            }
            outputDiv.textContent = '服务重启成功';
            await updateServiceStatus();
        } catch (error) {
            console.error('重启失败:', error);
            outputDiv.textContent = error.message;
        }
    });

    logoutButton.addEventListener('click', async () => {
        try {
            const response = await fetch('/logout', {
                method: 'POST'
            });
            if (response.ok) {
                window.location.href = '/login';
            } else {
                throw new Error('登出失败：' + response.statusText);
            }
        } catch (error) {
            console.error('登出失败:', error);
            outputDiv.textContent = error.message;
        }
    });

    openAddRuleButton.addEventListener('click', () => {
        resetRuleForm();
        openModal(ruleModal, localPortInput);
    });
    openBatchRulesButton.addEventListener('click', () => {
        showModalMessage(batchModalMessage);
        openModal(batchRulesModal, rulesInput);
    });
    addRuleButton.addEventListener('click', addRule);
    cancelEditButton.addEventListener('click', closeRuleModal);
    addBatchRulesButton.addEventListener('click', addBatchRules);
    closeRuleModalButton.addEventListener('click', closeRuleModal);
    closeBatchModalButton.addEventListener('click', closeBatchModal);
    cancelBatchButton.addEventListener('click', closeBatchModal);
    extraRemotesInput.addEventListener('input', updateBalanceFields);
    deleteSelectedButton.addEventListener('click', deleteSelectedRules);
    copySelectedButton.addEventListener('click', copySelectedRules);
    clearTrafficButton.addEventListener('click', clearSelectedTraffic);
    selectAllRules.addEventListener('change', () => {
        allRules.forEach(rule => {
            if (!rule.listen) return;
            if (selectAllRules.checked) {
                selectedRules.set(rule.listen, rule);
            } else {
                selectedRules.delete(rule.listen);
            }
        });
        renderForwardingRules();
    });

    [ruleModal, batchRulesModal].forEach(modal => {
        modal.addEventListener('click', event => {
            if (event.target === modal) {
                if (modal === ruleModal) {
                    closeRuleModal();
                } else {
                    closeBatchModal();
                }
            }
        });
    });

    document.addEventListener('keydown', event => {
        if (event.key !== 'Escape') return;
        if (!ruleModal.hidden) {
            closeRuleModal();
        } else if (!batchRulesModal.hidden) {
            closeBatchModal();
        }
    });

    document.getElementById('prevPage').addEventListener('click', goToPrevPage);
    document.getElementById('nextPage').addEventListener('click', goToNextPage);

    pageSizeSelect.addEventListener('change', () => {
        pageSize = parseInt(pageSizeSelect.value, 10);
        currentPage = 1;
        fetchForwardingRules();
    });

    updateBalanceFields();
    fetchForwardingRules();
    updateServerIP();
    updateServiceStatus();
    updateTrafficSpeed();
    setInterval(updateTrafficSpeed, 3000);
    document.addEventListener('visibilitychange', () => {
        if (!document.hidden) updateTrafficSpeed();
    });
    
    setInterval(updateServiceStatus, 15000);
    setInterval(() => {
        if (!document.hidden && ruleModal.hidden && batchRulesModal.hidden) {
            fetchForwardingRules(currentPage);
        }
    }, 15000);
});
