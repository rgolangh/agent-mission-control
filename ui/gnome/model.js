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

// The first pending ask, and its question if it is a single one the menu can
// render. `several` marks an ask with several questions, answered in the
// agent's own terminal.
export function firstQuestion(w) {
    const ask = w.pending && w.pending.length ? w.pending[0] : null;
    const n = ask && ask.questions ? ask.questions.length : 0;
    return {ask, question: n === 1 ? ask.questions[0] : null, several: n > 1};
}

export function canInput(w) {
    return !!(w.capabilities && w.capabilities.can_receive_input && !w.stale);
}

// A follow-up message is possible once the agent is idle and not waiting on
// an ask (the daemon refuses a send while one is pending).
export function canFollowUp(w) {
    return canInput(w) && !(w.pending && w.pending.length) && w.status !== 'running';
}

// Toggles an option in a multi-select pick list, keeping the question's
// option order, so the answer reads the way the agent listed them.
export function togglePick(picked, option, options) {
    const set = new Set(picked);
    if (set.has(option))
        set.delete(option);
    else
        set.add(option);
    return options.filter(o => set.has(o));
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

// Strips C0/C1 control characters, bidi overrides and zero-width characters
// (as the daemon does, see adapter.CleanText) and caps length: agent text
// goes into labels and notifications as data, never formatting.
export function clean(text, max = 300) {
    // eslint-disable-next-line no-control-regex
    const s = String(text ?? '').replace(/[\u0000-\u0008\u000b-\u001f\u007f-\u009f\u061c\u200b\u200e\u200f\u202a-\u202e\u2060\u2066-\u2069\ufeff]/g, '');
    return s.length > max ? `${s.slice(0, max - 1)}…` : s;
}

export const SERVICE = 'agent-mission-control.service';
export const RELEASES = 'https://github.com/rgolangh/agent-mission-control/releases';

// Why the daemon doesn't answer, from what the extension can observe: the
// HTTP status (0: no token file so no request was made, or nothing
// answered), whether a token was read, and whether an amc binary, its
// systemd user unit and the daemon's socket exist. Returns {state, summary,
// hint, action}; action is {label, kind: 'start'|'restart'|'copy'|'open',
// arg} or null. Mirrored in ui/omarchy/Api.qml (daemonState).
export function daemonState({status, token, installed, unit, socket, url, tokenPath}) {
    const start = `systemctl --user start ${SERVICE}`;
    const restart = `systemctl --user restart ${SERVICE}`;
    const logs = `Logs: journalctl --user -u ${SERVICE}`;
    const restartAction = {label: 'Restart daemon', kind: 'restart', arg: restart};
    if (status === 200)
        return {state: 'up', summary: '', hint: '', action: null};
    if (status === 401) {
        return {
            state: 'unauthorized',
            summary: 'daemon refused the token',
            hint: `The token in ${tokenPath} doesn't match the daemon at ${url}. Every start writes a fresh one:\n${restart}`,
            action: restartAction,
        };
    }
    if (status)
        return {state: 'error', summary: `daemon returned ${status}`, hint: logs, action: restartAction};
    if (token) {
        // A daemon started (it wrote the token) but nothing answers on TCP.
        return {
            state: 'unreachable',
            summary: `daemon not answering at ${url}`,
            hint: `It may have crashed, or listen elsewhere (serve -listen).\n${restart}\n${logs}`,
            action: restartAction,
        };
    }
    if (socket) {
        return {
            state: 'no-token',
            summary: "can't read the daemon's token",
            hint: `Expected ${tokenPath} (rewritten on every start, readable only by you). A restart writes it:\n${restart}`,
            action: restartAction,
        };
    }
    if (!installed && !unit) {
        return {
            state: 'not-installed',
            summary: 'amc is not installed',
            hint: `Install your distro package or a release from\n${RELEASES}\nthen run: amc install --enable`,
            action: {label: 'Open releases', kind: 'open', arg: RELEASES},
        };
    }
    if (!unit) {
        return {
            state: 'not-set-up',
            summary: 'amc is installed but not set up',
            hint: 'Run: amc install --enable\n(installs and starts the user service)',
            action: {label: 'Copy command', kind: 'copy', arg: 'amc install --enable'},
        };
    }
    return {
        state: 'not-running',
        summary: 'daemon is not running',
        hint: `Start it: ${start}\n${logs}`,
        action: {label: 'Start daemon', kind: 'start', arg: start},
    };
}
