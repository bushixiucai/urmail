"""Isolated Linux fixture: verify fixed FastCGI routes, denials and 128M limit.

Run only in a disposable no-network container with the template copied to /tmp.
The FastCGI fixture reports parameters; it does not execute Roundcube/PHP.
"""
import http.client
import json
import os
from pathlib import Path
import socket
import struct
import subprocess
import threading
import time


def receive(connection, size):
    data = b''
    while len(data) < size:
        part = connection.recv(size - len(data))
        if not part:
            raise EOFError('FastCGI connection closed')
        data += part
    return data


def length(data, offset):
    if data[offset] & 128:
        return struct.unpack_from('!I', data, offset)[0] & 0x7fffffff, offset + 4
    return data[offset], offset + 1


def fastcgi(server):
    while True:
        connection, _ = server.accept()
        try:
            parameters = b''
            while True:
                _, kind, request, size, padding, _ = struct.unpack('!BBHHBB', receive(connection, 8))
                content = receive(connection, size)
                receive(connection, padding)
                if kind == 4:
                    parameters += content
                if kind == 5 and not size:
                    break
            values = {}
            offset = 0
            while offset < len(parameters):
                namesize, offset = length(parameters, offset)
                valuesize, offset = length(parameters, offset)
                name = parameters[offset:offset + namesize].decode()
                offset += namesize
                value = parameters[offset:offset + valuesize].decode()
                offset += valuesize
                values[name] = value
            output = b'Content-Type: application/json\r\n\r\n' + json.dumps({key: values.get(key, '') for key in ['SCRIPT_FILENAME', 'SCRIPT_NAME', 'PATH_INFO', 'CONTENT_LENGTH']}).encode()
            connection.sendall(struct.pack('!BBHHBB', 1, 6, request, len(output), 0, 0) + output)
            connection.sendall(struct.pack('!BBHHBB', 1, 3, request, 8, 0, 0) + b'\0' * 8)
        except (EOFError, BrokenPipeError):
            pass
        finally:
            connection.close()


def main():
    if os.environ.get('URMAIL_V80_DISPOSABLE_FIXTURE') != 'true' or not Path('/tmp/nginx-v80.conf').is_file():
        raise SystemExit('Use the explicit disposable container fixture only')
    runtime = Path('/usr/local/lib/roundcubemail/public_html')
    runtime.mkdir(parents=True)
    Path('/run/php').mkdir(parents=True, exist_ok=True)
    socketpath = '/run/php/urmail-roundcube.sock'
    server = socket.socket(socket.AF_UNIX)
    server.bind(socketpath)
    os.chmod(socketpath, 0o777)
    server.listen()
    threading.Thread(target=fastcgi, args=(server,), daemon=True).start()
    Path('/tmp/fastcgi_params').symlink_to('/etc/nginx/fastcgi_params')
    config = Path('/tmp/nginx-fixture.conf')
    config.write_text('pid /tmp/nginx-v80.pid;\nerror_log /tmp/nginx-v80-error.log;\nevents{}\nhttp { include /tmp/nginx-v80.conf; }\n')
    subprocess.run(['nginx', '-t', '-c', str(config)], check=True)
    process = subprocess.Popen(['nginx', '-c', str(config), '-g', 'daemon off;'], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    def request(path, body=None, headers=None):
        connection = http.client.HTTPConnection('127.0.0.1', 8888, timeout=20)
        try:
            connection.request('POST' if body is not None else 'GET', path, body, headers or {})
            response = connection.getresponse()
            return response.status, response.read()
        finally:
            connection.close()
    try:
        for _ in range(50):
            try:
                status, _ = request('/mail/')
                assert status == 200
                break
            except ConnectionRefusedError:
                time.sleep(0.1)
        else:
            raise AssertionError('Disposable nginx did not start')
        assertions = []
        for path, script, info in [('/mail/index.php?_task=mail', 'index.php', ''), ('/mail/static.php/skins/elastic/styles/styles.css', 'static.php', '/skins/elastic/styles/styles.css'), ('/mail/plugins/urmail_bridge/bridge.js', 'static.php', '/plugins/urmail_bridge/bridge.js'), ('/mail/program/js/app.js', 'static.php', '/program/js/app.js')]:
            status, body = request(path)
            assert status == 200, (path, status, body[:200])
            result = json.loads(body)
            assert status == 200 and result['SCRIPT_FILENAME'] == str(runtime / script) and result['PATH_INFO'] == info, (path, status, result)
            assert result['SCRIPT_NAME'] == '/mail/' + script
            assertions.append({'path': path, 'status': status, 'fixed_entrypoint': script, 'path_info': info})
        for path in ['/mail/config/config.inc.php', '/mail/vendor/autoload.php', '/mail/installer/index.php', '/mail/arbitrary.php', '/mail/static.php/../../config/config.inc.php', '/mail/SQL/postgres.initial.sql']:
            status, _ = request(path)
            assert status in (403, 404), (path, status)
            assertions.append({'path': path, 'denied': True})
        # Exceed the old managed 25M limit with a real body; no attachment or
        # external SMTP is involved in this routing fixture.
        status, body = request('/mail/index.php', b'x' * (26 * 1024 * 1024))
        assert status == 200 and json.loads(body)['CONTENT_LENGTH'] == str(26 * 1024 * 1024)
        connection = http.client.HTTPConnection('127.0.0.1', 8888, timeout=10)
        connection.putrequest('POST', '/mail/index.php')
        connection.putheader('Content-Length', str(128 * 1024 * 1024 + 1))
        connection.endheaders()
        assert connection.getresponse().status == 413
        connection.close()
        print(json.dumps({'fixed_routes_and_denials': assertions, 'actual_26MiB_body_accepted': True, 'over_128MiB_rejected': True, 'real_roundcube_static_allowlist_not_executed': True}, indent=2))
    finally:
        process.terminate()
        process.wait(timeout=10)
        server.close()


if __name__ == '__main__':
    main()
