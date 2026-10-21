// gjs -m ui/gnome/test/client.test.js   (needs a running daemon)
import Gio from 'gi://Gio';
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

        const tmp = GLib.dir_make_tmp('mc-client-XXXXXX');
        const wrong = new Client();
        wrong._tokenFile = Gio.File.new_for_path(`${tmp}/token`);
        GLib.file_set_contents(`${tmp}/token`, 'not-the-token\n');
        const r2 = await wrong.workflows();
        check(r2.status === 401, `a wrong token is refused (${r2.status})`);

        // Daemon down (no token file) and something else on its port: the
        // client must not talk to it at all.
        let connections = 0;
        const squatter = new Gio.SocketService();
        const port = squatter.add_any_inet_port(null);
        squatter.connect('incoming', () => {
            connections++;
            return false;
        });
        squatter.start();
        const none = new Client(`http://127.0.0.1:${port}`);
        none._tokenFile = Gio.File.new_for_path(`${tmp}/no-such-token`);
        const r3 = await none.workflows();
        await none.answer('ses_x', 'q', 'yes');
        squatter.stop();
        check(r3.status === 0 && connections === 0, `no token file: no request at all (${r3.status}, ${connections} connection(s))`);
        check(r3.token === false && r.token === true, 'results say whether a token was read');

        // probe() only looks at its own paths.
        none._binPaths = [`${tmp}/amc`];
        none._unitPaths = [`${tmp}/agent-mission-control.service`];
        none._socketPath = `${tmp}/api.sock`;
        const p0 = none.probe();
        check(!p0.unit && !p0.socket, 'probe: no unit, no socket');
        GLib.file_set_contents(`${tmp}/agent-mission-control.service`, '');
        GLib.file_set_contents(`${tmp}/api.sock`, '');
        const p1 = none.probe();
        check(p1.unit && p1.socket && p1.url === `http://127.0.0.1:${port}`, 'probe: unit and socket found');
        GLib.unlink(`${tmp}/agent-mission-control.service`);
        GLib.unlink(`${tmp}/api.sock`);
        GLib.unlink(`${tmp}/token`);
        GLib.rmdir(tmp);

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
