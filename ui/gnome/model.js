// Pure workflow logic shared by the GNOME extension's UI: no imports, so it
// runs (and is tested) in plain gjs. Mirrors ui/omarchy/Api.qml.

// Urgency order: what needs you first, then what might, then what is busy,
// then what is finished. Newest first within a group.
const RANK = {needs_input: 0, stuck: 1, failed: 2, running: 3, done: 4};

export function sortByUrgency(workflows) {
    return [...workflows].sort((a, b) => {
        const ra = RANK[a.status] ?? 5, rb = RANK[b.status] ?? 5;
        if (ra !== rb)
            return ra - rb;
        return a.updated_at < b.updated_at ? 1 : a.updated_at > b.updated_at ? -1 : 0;
    });
}

// Counts by status, ignoring stale workflows (their state is not current).
export function counts(workflows) {
    const c = {needs_input: 0, stuck: 0, failed: 0, running: 0, done: 0};
    for (const w of workflows) {
        if (!w.stale && w.status in c)
            c[w.status]++;
    }
    c.attention = c.needs_input + c.stuck + c.failed;
    return c;
}

export function summary(workflows) {
    const c = counts(workflows);
    const parts = [];
    if (c.needs_input)
        parts.push(`${c.needs_input} waiting on you`);
    if (c.stuck)
        parts.push(`${c.stuck} stuck`);
    if (c.failed)
        parts.push(`${c.failed} failed`);
    if (c.running)
        parts.push(`${c.running} running`);
    if (!parts.length)
        parts.push(workflows.length ? 'nothing needs you' : 'no workflows');
    return parts.join(' · ');
}

export function needsYou(w) {
    return !w.stale && ['needs_input', 'stuck', 'failed'].includes(w.status);
}

// The daemon emits RFC 3339 with nanoseconds; JS dates take milliseconds.
export function parseTime(t) {
    return Date.parse(String(t || '').replace(/(\.\d{3})\d+/, '$1'));
}

export function age(t, now = Date.now()) {
    const ms = parseTime(t);
    if (Number.isNaN(ms))
        return '';
    const s = Math.max(0, Math.round((now - ms) / 1000));
    if (s < 60)
        return 'now';
    if (s < 3600)
        return `${Math.floor(s / 60)}m`;
    if (s < 86400)
        return `${Math.floor(s / 3600)}h`;
    return `${Math.floor(s / 86400)}d`;
}

export function tilde(path, home) {
    if (!path)
        return '';
    return home && path.startsWith(home) ? `~${path.slice(home.length)}` : path;
}

export function statusLabel(w) {
    const s = (w.status || 'unknown').replace('_', ' ');
    return w.stale ? `${s} · stale` : s;
}

// The first pending ask, if it is a single question the menu can render.
export function firstQuestion(w) {
    const ask = w.pending && w.pending.length ? w.pending[0] : null;
    const q = ask && ask.questions && ask.questions.length === 1 ? ask.questions[0] : null;
    return {ask, question: q};
}

export function canInput(w) {
    return !!(w.capabilities && w.capabilities.can_receive_input && !w.stale);
}

// Workflows that newly need you since the previous poll, so each transition
// notifies once. `previous` maps workflow id -> signature; returns the new map.
export function newlyNeedingYou(workflows, previous) {
    const next = {}, fresh = [];
    for (const w of workflows) {
        const sig = `${w.status}|${(w.pending || []).map(a => a.id).join(',')}`;
        next[w.id] = sig;
        if (previous && needsYou(w) && previous[w.id] !== sig)
            fresh.push(w);
    }
    return {next, fresh};
}

// Strips C0/C1 control characters and caps length: agent text goes into
// labels and notifications as data, never formatting.
export function clean(text, max = 300) {
    // eslint-disable-next-line no-control-regex
    const s = String(text ?? '').replace(/[\u0000-\u0008\u000b-\u001f\u007f-\u009f]/g, '');
    return s.length > max ? `${s.slice(0, max - 1)}…` : s;
}
