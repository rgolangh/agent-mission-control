// gjs -m ui/gnome/test/client.test.js   (needs a running daemon)
import GLib from 'gi://GLib';
import {Client} from '../client.js';

const loop = new GLib.MainLoop(null, false);
let failed = 0;
const check = (ok, what) => {
    if (!ok)
        failed++;
    print(`${ok ? 'ok  ' : 'FAIL'} ${what}`);
};

(async () => {
    try {
        const c = new Client();
        const r = await c.workflows();
        check(r.status === 200 && Array.isArray(r.workflows), `authorized with the token file (${r.workflows.length} workflows)`);

        c._token = 'stale-token-from-a-previous-daemon';
        const r2 = await c.workflows();
        check(r2.status === 200, 'recovers from a stale token by re-reading the file');

        const bad = new Client();
        bad._tokenFile = bad._tokenFile.get_parent().get_child('no-such-token');
        const r3 = await bad.workflows();
        check(r3.status === 401, `without a token the daemon refuses (${r3.status})`);

        const down = new Client('http://127.0.0.1:1');
        const r4 = await down.workflows();
        check(r4.status === 0, 'unreachable daemon reports status 0');

        const r5 = await c.answer('no-such-workflow', 'x', 'y');
        check(r5.status === 404 && r5.data && r5.data.error, `answer errors come back with a message (${r5.status}: ${r5.data && r5.data.error})`);
    } catch (e) {
        failed++;
        printerr(e);
    }
    loop.quit();
})();
loop.run();
print(failed ? `${failed} failure(s)` : 'client: all passed');
if (failed)
    imports.system.exit(1);
