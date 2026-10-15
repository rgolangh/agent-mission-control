// HTTP client for the mission-control daemon, for GNOME Shell (and plain
// gjs, where it is tested). The daemon's TCP port requires the bearer token
// it writes to $XDG_RUNTIME_DIR/mission-control/token on every start, so the
// token is re-read whenever a request is refused.
import Gio from 'gi://Gio';
import GLib from 'gi://GLib';
import Soup from 'gi://Soup?version=3.0';

Gio._promisify(Soup.Session.prototype, 'send_and_read_async');
Gio._promisify(Gio.File.prototype, 'load_contents_async');

export class Client {
    constructor(baseUrl = 'http://127.0.0.1:4200') {
        this.baseUrl = baseUrl.replace(/\/$/, '');
        this._session = new Soup.Session({timeout: 6});
        this._token = '';
        const runtime = GLib.getenv('XDG_RUNTIME_DIR') || '';
        this._tokenFile = Gio.File.new_for_path(`${runtime}/mission-control/token`);
    }

    async _loadToken(cancellable) {
        try {
            const [bytes] = await this._tokenFile.load_contents_async(cancellable);
            this._token = new TextDecoder().decode(bytes).trim();
        } catch {
            this._token = '';
        }
    }

    // Returns {status, data}; status 0 means the daemon is unreachable.
    async request(method, path, body = undefined, cancellable = null, retry = true) {
        if (!this._token)
            await this._loadToken(cancellable);
        const msg = Soup.Message.new(method, this.baseUrl + path);
        if (this._token)
            msg.request_headers.append('Authorization', `Bearer ${this._token}`);
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
            return {status: 0, data: null};
        }
        const status = msg.get_status();
        if (status === 401 && retry) {
            // The daemon restarted with a new token.
            this._token = '';
            return this.request(method, path, body, cancellable, false);
        }
        let data = null;
        try {
            data = JSON.parse(text);
        } catch {}
        return {status, data};
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
