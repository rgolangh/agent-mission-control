// HTTP client for the agent-mission-control daemon, for GNOME Shell (and plain
// gjs, where it is tested). The daemon's TCP port requires the bearer token
// it writes to $XDG_RUNTIME_DIR/agent-mission-control/token on every start and
// removes when it stops. The token is re-read before every request, and with
// no token file there is no request: while the daemon is down, whatever else
// holds its port gets neither a token nor your answers.
import Gio from 'gi://Gio';
import GLib from 'gi://GLib';
import Soup from 'gi://Soup?version=3.0';

Gio._promisify(Soup.Session.prototype, 'send_and_read_async');
Gio._promisify(Gio.File.prototype, 'load_contents_async');

export class Client {
    constructor(baseUrl = 'http://127.0.0.1:4200') {
        this.baseUrl = baseUrl.replace(/\/$/, '');
        this._session = new Soup.Session({timeout: 6});
        const runtime = GLib.getenv('XDG_RUNTIME_DIR') || '';
        this.tokenPath = `${runtime}/agent-mission-control/token`;
        this._tokenFile = Gio.File.new_for_path(this.tokenPath);
        this._socketPath = `${runtime}/agent-mission-control/api.sock`;
        const home = GLib.get_home_dir();
        // Where `amc install` and distro packages put the binary and unit
        // (GNOME Shell's PATH often lacks ~/.local/bin).
        this._binPaths = [`${home}/.local/bin/amc`, '/usr/local/bin/amc', '/usr/bin/amc'];
        this._unitPaths = ['.config/systemd/user', '/etc/systemd/user', '/usr/lib/systemd/user']
            .map(d => `${d.startsWith('/') ? d : `${home}/${d}`}/agent-mission-control.service`);
    }

    // What is on disk, for explaining a daemon that doesn't answer (see
    // model.daemonState). Cheap stat calls, only made while it is down.
    probe() {
        const exists = p => GLib.file_test(p, GLib.FileTest.EXISTS);
        return {
            installed: !!GLib.find_program_in_path('amc') || this._binPaths.some(exists),
            unit: this._unitPaths.some(exists),
            socket: exists(this._socketPath),
            url: this.baseUrl,
            tokenPath: this.tokenPath,
        };
    }

    async _token(cancellable) {
        try {
            const [bytes] = await this._tokenFile.load_contents_async(cancellable);
            return new TextDecoder().decode(bytes).trim();
        } catch (e) {
            if (e.matches?.(Gio.IOErrorEnum, Gio.IOErrorEnum.CANCELLED))
                throw e;
            return '';
        }
    }

    // Returns {status, data, token}; status 0 means the daemon is not
    // running (no token file: token is false) or unreachable.
    async request(method, path, body = undefined, cancellable = null) {
        const token = await this._token(cancellable);
        if (!token)
            return {status: 0, data: null, token: false};
        const msg = Soup.Message.new(method, this.baseUrl + path);
        msg.request_headers.append('Authorization', `Bearer ${token}`);
        if (body !== undefined) {
            // The daemon refuses POSTs that are not JSON (CSRF guard).
            const bytes = new TextEncoder().encode(JSON.stringify(body));
            msg.set_request_body_from_bytes('application/json', new GLib.Bytes(bytes));
        }
        let text = '';
        try {
            const bytes = await this._session.send_and_read_async(msg, GLib.PRIORITY_DEFAULT, cancellable);
            text = new TextDecoder().decode(bytes.get_data() ?? new Uint8Array());
        } catch (e) {
            if (e.matches?.(Gio.IOErrorEnum, Gio.IOErrorEnum.CANCELLED))
                throw e;
            return {status: 0, data: null, token: true};
        }
        const status = msg.get_status();
        let data = null;
        try {
            data = JSON.parse(text);
        } catch {}
        return {status, data, token: true};
    }

    async workflows(cancellable = null) {
        const r = await this.request('GET', '/workflows', undefined, cancellable);
        return {...r, workflows: r.status === 200 && r.data ? r.data.workflows || [] : []};
    }

    // ref: the workflow's source_id (no ':' to escape in a path). choice is
    // an option label or free text; an array is several picks for one
    // multi-select question (sent as `answers`, so labels may contain commas).
    answer(ref, askId, choice, cancellable = null) {
        const body = Array.isArray(choice) ? {ask_id: askId, answers: [choice]} : {ask_id: askId, choice};
        return this.request('POST', `/workflows/${encodeURIComponent(ref)}/answer`, body, cancellable);
    }

    send(ref, text, cancellable = null) {
        return this.request('POST', `/workflows/${encodeURIComponent(ref)}/send`, {text}, cancellable);
    }

    destroy() {
        this._session.abort();
        this._session = null;
    }
}
