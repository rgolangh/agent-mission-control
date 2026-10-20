// gjs -m ui/gnome/test/schema.test.js
import Gio from 'gi://Gio';
import GLib from 'gi://GLib';

const schemaDir = GLib.build_filenamev([
    GLib.get_current_dir(), 'ui', 'gnome', 'schemas',
]);
const metadataPath = GLib.build_filenamev([
    GLib.get_current_dir(), 'ui', 'gnome', 'metadata.json',
]);
const [metadataLoaded, metadataBytes] = GLib.file_get_contents(metadataPath);
if (!metadataLoaded)
    throw new Error(`could not read extension metadata: ${metadataPath}`);
const schemaId = 'org.gnome.shell.extensions.mission-control';
const metadata = JSON.parse(new TextDecoder().decode(metadataBytes));
if (metadata['settings-schema'] !== schemaId)
    throw new Error(`metadata settings-schema must be ${schemaId}`);

const source = Gio.SettingsSchemaSource.new_from_directory(
    schemaDir, Gio.SettingsSchemaSource.get_default(), false);
const schema = source.lookup(schemaId, true);
if (!schema)
    throw new Error('Mission Control GSettings schema is missing');

const shortcut = schema.get_key('toggle-menu').get_default_value().deep_unpack();
if (JSON.stringify(shortcut) !== JSON.stringify(['<Super><Alt>m']))
    throw new Error(`unexpected toggle-menu default: ${JSON.stringify(shortcut)}`);

print('schema: all passed');
