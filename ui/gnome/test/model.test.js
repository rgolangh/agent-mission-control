// gjs -m ui/gnome/test/model.test.js
import * as m from '../model.js';

let failed = 0;
const eq = (got, want, what) => {
    const ok = JSON.stringify(got) === JSON.stringify(want);
    if (!ok) {
        failed++;
        printerr(`FAIL ${what}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
    }
};

const wfs = [
    {id: 'a', status: 'done', updated_at: '2026-10-14T10:00:00.123456789Z'},
    {id: 'b', status: 'needs_input', updated_at: '2026-10-14T09:00:00Z', pending: [{id: 'q1', questions: [{question: 'Which?', options: ['x', 'y']}]}], capabilities: {can_receive_input: true}},
    {id: 'c', status: 'running', updated_at: '2026-10-14T11:00:00Z'},
    {id: 'd', status: 'stuck', updated_at: '2026-10-14T08:00:00Z'},
    {id: 'e', status: 'needs_input', updated_at: '2026-10-14T12:00:00Z', stale: true},
];
eq(m.sortByUrgency(wfs).map(w => w.id), ['e', 'b', 'd', 'c', 'a'], 'urgency order');
eq(m.counts(wfs), {needs_input: 1, stuck: 1, failed: 0, running: 1, done: 1, attention: 2}, 'counts skip stale');
eq(m.summary(wfs), '1 waiting on you · 1 stuck · 1 running', 'summary');
eq(m.summary([]), 'no workflows', 'empty summary');
eq(m.age('2026-10-14T10:00:00.123456789Z', Date.parse('2026-10-14T10:05:00Z')), '5m', 'nanosecond timestamps (4m59.9s rounds to 5m)');
eq(m.tilde('/home/u/src/x', '/home/u'), '~/src/x', 'tilde');
eq(m.statusLabel(wfs[4]), 'needs input · stale', 'stale label');
eq(m.firstQuestion(wfs[1]).question.question, 'Which?', 'first question');
eq(m.firstQuestion(wfs[1]).several, false, 'one question is not several');
const multiQ = {...wfs[1], pending: [{id: 'q2', questions: [{question: 'A?'}, {question: 'B?'}]}]};
eq([m.firstQuestion(multiQ).question, m.firstQuestion(multiQ).several], [null, true], 'several questions: no single question to render');
eq(m.canInput(wfs[1]), true, 'can input');
eq(m.canFollowUp(wfs[1]), false, 'no follow-up while an ask is pending');
eq(m.canFollowUp(multiQ), false, 'no follow-up while a multi-question ask is pending');
eq(m.canFollowUp({...wfs[1], status: 'done', pending: []}), true, 'follow-up once idle');
eq(m.canFollowUp({...wfs[1], status: 'running', pending: []}), false, 'no follow-up while running');
eq(m.togglePick([], 'c', ['a', 'b', 'c']), ['c'], 'pick');
eq(m.togglePick(['c'], 'a', ['a', 'b', 'c']), ['a', 'c'], 'picks keep option order');
eq(m.togglePick(['a', 'c'], 'a', ['a', 'b', 'c']), ['c'], 'unpick');
eq(m.canInput({...wfs[1], stale: true}), false, 'stale cannot input');
eq(m.clean('\u001b]0;pwned\u0007ok\nline'), ']0;pwnedok\nline', 'control chars stripped, newline kept');
eq(m.clean('x'.repeat(10), 5), 'xxxx…', 'length cap');
eq(m.clean('run \u202etxt.exe\u200b'), 'run txt.exe', 'bidi override and zero-width stripped');

const first = m.newlyNeedingYou(wfs, null);
eq(first.fresh.length, 0, 'first poll notifies nothing');
const again = m.newlyNeedingYou(wfs, first.next);
eq(again.fresh.length, 0, 'unchanged poll notifies nothing');
const changed = m.newlyNeedingYou([{...wfs[2], status: 'stuck'}, ...wfs.slice(0, 2)], again.next);
eq(changed.fresh.map(w => w.id), ['c'], 'a workflow that became stuck notifies once');

// Daemon down: what the menu says, from what it can observe.
const local = {installed: true, unit: true, socket: false, url: 'http://127.0.0.1:4200', tokenPath: '/run/user/1/agent-mission-control/token'};
const ds = o => m.daemonState({...local, ...o});
const cases = [
    [{status: 200, token: true}, 'up', null],
    [{status: 401, token: true}, 'unauthorized', 'restart'],
    [{status: 500, token: true}, 'error', 'restart'],
    [{status: 0, token: true}, 'unreachable', 'restart'],
    [{status: 0, token: false, socket: true}, 'no-token', 'restart'],
    [{status: 0, token: false, installed: false, unit: false}, 'not-installed', 'open'],
    [{status: 0, token: false, unit: false}, 'not-set-up', 'copy'],
    [{status: 0, token: false, installed: false}, 'not-running', 'start'],
    [{status: 0, token: false}, 'not-running', 'start'],
];
for (const [o, state, kind] of cases) {
    const d = ds(o);
    eq([d.state, d.action && d.action.kind], [state, kind], `daemon state ${JSON.stringify(o)}`);
}
eq(ds({status: 0, token: false}).hint.includes('journalctl --user -u agent-mission-control.service'), true, 'not running: points at the logs');
eq(ds({status: 0, token: false, installed: false, unit: false}).hint.includes(m.RELEASES), true, 'not installed: points at the releases');
eq(ds({status: 401, token: true}).hint.includes(local.tokenPath), true, 'unauthorized: names the token file');
eq(ds({status: 0, token: false}).action.arg, 'systemctl --user start agent-mission-control.service', 'start command');

print(failed ? `${failed} failure(s)` : 'model: all passed');
if (failed)
    imports.system.exit(1);
