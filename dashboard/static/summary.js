(() => {
  const qs = (s, r=document) => r.querySelector(s);
  const fmt = (d) => isNaN(d.getTime()) ? 'never' : d.toLocaleString();

  const relativeTime = (iso) => {
    if (!iso) return 'never';
    const d = new Date(iso);
    if (isNaN(d.getTime())) return 'never';
    const now = Date.now();
    const diff = now - d.getTime();
    if (diff < 0) return fmt(d);
    const s = Math.floor(diff / 1000);
    if (s < 60) return s <= 1 ? 'just now' : s + 's ago';
    const m = Math.floor(s / 60);
    if (m < 60) return m === 1 ? '1m ago' : m + 'm ago';
    const h = Math.floor(m / 60);
    if (h < 24) return h === 1 ? '1h ago' : h + 'h ago';
    const days = Math.floor(h / 24);
    return days === 1 ? '1d ago' : days + 'd ago';
  };

  const truncate = (s, n) => {
    if (!s) return '';
    return s.length > n ? s.slice(0, n) + '…' : s;
  };

  const renderSuggested = (data) => {
    const s = data.suggested_next;
    const el = qs('#suggested');
    if (!s) {
      el.innerHTML = '<em>nothing queued — idle-deck has no next move</em>';
      return;
    }
    el.innerHTML = `
      <div><span class="label">Task:</span> <span class="value"><code>${s.task_id.slice(0, 12)}</code></span></div>
      <div><span class="label">Tier:</span> <span class="value tier-${s.tier}">${s.tier}</span></div>
      <div><span class="label">State:</span> <span class="value state-${s.state}">${s.state}</span></div>
      <div><span class="label">Prompt:</span> <span class="value">${truncate(s.prompt, 80)}</span></div>
      ${s.ticket_url ? `<div><span class="label">Ticket:</span> <span class="value"><a href="${s.ticket_url}" target="_blank">link</a></span></div>` : ''}
    `;
  };

  const renderConcluded = (data) => {
    const items = data.concluded || [];
    qs('#concluded-count').textContent = `(${items.length})`;
    const tbody = qs('#concluded tbody');
    tbody.innerHTML = '';
    if (items.length === 0) {
      tbody.innerHTML = '<tr><td colspan="6" class="empty">no concluded runs since your last review</td></tr>';
      return;
    }
    items.forEach(c => {
      const tr = document.createElement('tr');
      const prBadge = c.pr_state ? ` <span class="badge badge-${c.pr_state}">${c.pr_state}</span>` : '';
      tr.innerHTML = `
        <td><code>${c.task_id.slice(0, 8)}</code></td>
        <td class="tier-${c.tier}">${c.tier}</td>
        <td class="outcome-${c.outcome.replace(/_/g, '-')}">${c.outcome}</td>
        <td>${relativeTime(c.finished_at)}<span title="${fmt(new Date(c.finished_at))}">&nbsp;</span></td>
        <td>${c.ticket_url ? `<a href="${c.ticket_url}" target="_blank">link</a>` : '—'}</td>
        <td>${c.pr_url ? `<a href="${c.pr_url}" target="_blank">link</a>${prBadge}` : '—'}</td>
      `;
      tbody.appendChild(tr);
    });
  };

  const renderAwaiting = (data) => {
    const items = data.awaiting || [];
    qs('#awaiting-count').textContent = `(${items.length})`;
    const tbody = qs('#awaiting tbody');
    tbody.innerHTML = '';
    if (items.length === 0) {
      tbody.innerHTML = '<tr><td colspan="6" class="empty">nothing is waiting on you</td></tr>';
      return;
    }
    items.forEach(a => {
      const tr = document.createElement('tr');
      tr.innerHTML = `
        <td><code>${a.task_id.slice(0, 8)}</code></td>
        <td class="tier-${a.tier}">${a.tier}</td>
        <td class="outcome-${a.reason.replace(/_/g, '-')}">${a.reason}</td>
        <td>${relativeTime(a.finished_at)}<span title="${fmt(new Date(a.finished_at))}">&nbsp;</span></td>
        <td>${a.retry_count}×</td>
        <td>${a.ticket_url ? `<a href="${a.ticket_url}" target="_blank">link</a>` : '—'}</td>
      `;
      tbody.appendChild(tr);
    });
  };

  const renderQuestions = (data) => {
    const items = data.questions || [];
    qs('#questions-count').textContent = `(${items.length})`;
    const tbody = qs('#questions tbody');
    tbody.innerHTML = '';
    if (items.length === 0) {
      tbody.innerHTML = '<tr><td colspan="3" class="empty">no open questions</td></tr>';
      return;
    }
    items.forEach(q => {
      const tr = document.createElement('tr');
      tr.innerHTML = `
        <td>${q.repo}</td>
        <td>${q.number}</td>
        <td><a href="${q.url}" target="_blank">${q.title}</a></td>
      `;
      tbody.appendChild(tr);
    });
  };

  const renderLastSeen = (data) => {
    const el = qs('#last-seen');
    const btn = qs('#mark-seen');
    if (!data.last_seen) {
      el.textContent = 'never';
      btn.textContent = 'Mark all as read';
    } else {
      el.textContent = relativeTime(data.last_seen);
      btn.textContent = 'Mark as read';
    }
    btn.disabled = false;
  };

  const render = (data) => {
    qs('#header-meta').textContent = `generated ${new Date(data.generated_at).toLocaleTimeString()}`;
    qs('#updated').textContent = `updated ${new Date(data.generated_at).toLocaleString()}`;
    renderLastSeen(data);
    renderSuggested(data);
    renderConcluded(data);
    renderAwaiting(data);
    renderQuestions(data);
  };

  const load = async () => {
    try {
      const res = await fetch('/api/summary');
      if (!res.ok) throw new Error(res.status + ' ' + res.statusText);
      const data = await res.json();
      render(data);
    } catch (e) {
      qs('#header-meta').textContent = 'error: ' + e.message;
    }
  };

  const markSeen = async () => {
    const btn = qs('#mark-seen');
    btn.disabled = true;
    try {
      const res = await fetch('/api/summary/seen', { method: 'POST' });
      if (!res.ok) throw new Error(res.status + ' ' + res.statusText);
      const data = await res.json();
      qs('#last-seen').textContent = relativeTime(data.seen_at);
      btn.textContent = 'Marked as read';
      btn.disabled = true;
      setTimeout(load, 300);
    } catch (e) {
      qs('#header-meta').textContent = 'error: ' + e.message;
      btn.disabled = false;
    }
  };

  qs('#mark-seen').addEventListener('click', markSeen);
  load();
  setInterval(load, 15000);
})();