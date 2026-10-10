(() => {
  const qs = (s, r=document) => r.querySelector(s);
  const qsa = (s, r=document) => [...r.querySelectorAll(s)];
  const fmt = (d) => isNaN(d.getTime()) ? 'never' : d.toLocaleString();
  const fmtDuration = (s) => s;

  const poll = async () => {
    try {
      const res = await fetch('/api/status');
      if (!res.ok) throw new Error(res.status+' '+res.statusText);
      const data = await res.json();
      renderStatus(data);
    } catch (e) {
      qs('#header-meta').textContent = 'error: '+e.message;
    }
  };

  const renderStatus = (data) => {
    qs('#header-meta').textContent = `schema v${data.schema} • ${new Date(data.generated_at).toLocaleTimeString()}`;
    qs('#updated').textContent = `updated ${new Date(data.generated_at).toLocaleString()}`;

    // Heartbeat
    const hb = data.heartbeat;
    qs('#heartbeat').innerHTML = `
      <span class="label">Beat:</span> <span class="value">${fmt(new Date(hb.beat_at))}</span>
      <span class="badge ${hb.stale ? 'err' : 'ok'}">${hb.stale ? 'stale' : 'fresh'}</span>
      <span class="label" style="margin-left:12px">Lock:</span> <span class="value">${hb.lock_held ? 'held' : 'free'}</span>
    `;

    // Harness
    const hr = data.harness;
    qs('#harness').innerHTML = `
      <span class="label">URL:</span> <span class="value">${hr.url}</span><br>
      <span class="label">Reachable:</span> <span class="value">${hr.reachable ? 'yes' : 'no'}</span>
      ${hr.error ? `<br><span class="label">Error:</span> <span class="value">${hr.error}</span>` : ''}
    `;

    // Repos
    const repTbody = qs('#repos tbody');
    repTbody.innerHTML = '';
    data.repos.forEach(r => {
      const tr = document.createElement('tr');
      tr.innerHTML = `
        <td>${r.repo}</td>
        <td>${r.watermark}</td>
        <td>${fmt(new Date(r.last_poll))}</td>
      `;
      repTbody.appendChild(tr);
    });

    // Queue
    qs('#queue-total').innerHTML = Object.entries(data.queue.total_by_state)
      .map(([k,v]) => `<span class="label">${k}:</span> <span class="value">${v}</span> `).join('');
    const qTbody = qs('#queue tbody');
    qTbody.innerHTML = '';
    data.queue.by_state_tier.forEach(s => {
      const tr = document.createElement('tr');
      tr.innerHTML = `<td class="state-${s.state}">${s.state}</td><td class="tier-${s.tier}">${s.tier}</td><td>${s.count}</td>`;
      qTbody.appendChild(tr);
    });

    // Active
    const active = data.active;
    if (active) {
      qs('#active').innerHTML = `
        <div><span class="label">Task:</span> <span class="value"><code>${active.task_id}</code></span></div>
        <div><span class="label">Tier:</span> <span class="value tier-${active.tier}">${active.tier} (${active.role})</span></div>
        <div><span class="label">Attempt:</span> <span class="value">${active.attempt_id}</span></div>
        <div><span class="label">Running:</span> <span class="value">${active.elapsed}</span></div>
      `;
    } else {
      qs('#active').innerHTML = '<em>none</em>';
    }

    // Config
    qs('#config').textContent = data.config;
  };

  // Load attempts and artifacts on first load
  const loadAttempts = async () => {
    try {
      const res = await fetch('/api/attempts');
      const attempts = await res.json();
      const tbody = qs('#attempts tbody');
      tbody.innerHTML = '';
      attempts.forEach(a => {
        const tr = document.createElement('tr');
        tr.innerHTML = `
          <td><code>${a.task_id.slice(0,8)}</code></td>
          <td class="tier-${a.tier}">${a.tier}</td>
          <td>${a.ordinal}</td>
          <td class="outcome-${a.outcome.replace(/_/g,'-')}">${a.outcome}</td>
          <td>${a.started_at ? fmt(new Date(a.started_at)) : '—'}</td>
          <td>${a.finished_at ? fmt(new Date(a.finished_at)) : '—'}</td>
          <td>${a.ticket_url ? `<a href="${a.ticket_url}" target="_blank">link</a>` : '—'}</td>
        `;
        tbody.appendChild(tr);
      });
    } catch (e) {}
  };

  const loadArtifacts = async () => {
    try {
      const res = await fetch('/api/artifacts');
      const arts = await res.json();
      const tbody = qs('#artifacts tbody');
      tbody.innerHTML = '';
      arts.forEach(a => {
        const tr = document.createElement('tr');
        tr.innerHTML = `
          <td>${a.kind}</td>
          <td>${a.branch_purpose || '—'}</td>
          <td><a href="${a.uri}" target="_blank">${a.uri}</a></td>
          <td>${fmt(new Date(a.produced_at*1000))}</td>
        `;
        tbody.appendChild(tr);
      });
    } catch (e) {}
  };

  poll();
  loadAttempts();
  loadArtifacts();
  setInterval(poll, 5000);
})();