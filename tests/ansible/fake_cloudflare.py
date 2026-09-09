"""In-memory Cloudflare API double; never sends requests to Cloudflare."""

from collections import deque
import copy
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
import re
import threading
from urllib.parse import parse_qs, urlsplit


class FakeCloudflare:
    zone_id = 'a' * 32
    zone_name = 'example.test'
    token = 'disposable-test-token-with-no-external-authority'

    def __init__(self):
        self.records = {}
        self.next_id = 1
        self.requests = deque(maxlen=4096)
        self.mutations = deque(maxlen=4096)
        self.operations = deque(maxlen=4096)
        self.authorized = True
        self.denials = 0
        self.lock = threading.RLock()

    def seed(self, name, record_type, content, ttl=300):
        with self.lock:
            if len(self.records) >= 1000:
                raise ValueError('fake API record limit reached')
            identifier = f'{self.next_id:032x}'
            self.next_id += 1
            self.records[identifier] = dict(id=identifier, name=name, type=record_type, content=content, ttl=ttl,
                                            proxied=False, proxiable=record_type in {'A', 'AAAA', 'CNAME'}, comment='', tags=[])
            return identifier

    def snapshot(self):
        with self.lock:
            return copy.deepcopy(list(self.records.values()))

    def result(self, value):
        response = dict(success=True, errors=[], messages=[], result=value)
        if isinstance(value, list):
            response['result_info'] = dict(page=1, per_page=5000, count=len(value), total_count=len(value), total_pages=1)
        return 200, response

    def error(self, code=403):
        return code, dict(success=False, errors=[dict(code=10000, message='Disposable API refused the requested operation')], messages=[], result=None)

    def change(self, method, payload, identifier=None):
        if method == 'DELETE':
            if identifier not in self.records:
                raise ValueError('unknown record')
            self.operations.append(dict(method=method, name=self.records[identifier]['name'], type=self.records[identifier]['type']))
            del self.records[identifier]
            return {'id': identifier}
        if not isinstance(payload, dict) or payload.get('type') not in {'A', 'AAAA', 'CNAME', 'TXT'}:
            raise ValueError('unsupported record')
        name, content = payload.get('name', ''), payload.get('content', '')
        if not isinstance(name, str) or not isinstance(content, str) or len(name) > 253 or len(content) > 4096:
            raise ValueError('unbounded record')
        self.operations.append(dict(method=method, name=name, type=payload['type']))
        if name != self.zone_name and not name.endswith('.' + self.zone_name):
            raise ValueError('record outside zone')
        if method == 'POST':
            if any(record['name'] == name and record['type'] == payload['type'] and record['content'] == content for record in self.records.values()):
                raise ValueError('duplicate record')
            identifier = self.seed(name, payload['type'], content, payload.get('ttl', 1))
        elif identifier not in self.records:
            raise ValueError('unknown record')
        self.records[identifier].update({key: value for key, value in payload.items() if key in {'name', 'type', 'content', 'ttl', 'proxied', 'comment', 'tags'}})
        self.records[identifier]['id'] = identifier
        return copy.deepcopy(self.records[identifier])

    def handle(self, method, path, authorization, payload=None):
        parsed = urlsplit(path)
        query = parse_qs(parsed.query)
        path = parsed.path.rstrip('/')
        with self.lock:
            self.requests.append(dict(method=method, path=path, page=query.get('page', ['1'])[0][:16]))
            if not self.authorized or authorization != 'Bearer ' + self.token:
                self.denials += 1
                return self.error()
            prefix = '/client/v4/zones/' + self.zone_id
            if method == 'GET' and path == prefix:
                return self.result(dict(id=self.zone_id, name=self.zone_name, status='active', paused=False, type='full', plan=dict(name='Free')))
            if method == 'GET' and path == prefix + '/dns_records':
                try:
                    page = int(query.get('page', ['1'])[0])
                    per_page = int(query.get('per_page', ['5000'])[0])
                    if page < 1 or not 1 <= per_page <= 5000:
                        raise ValueError('invalid pagination')
                except (TypeError, ValueError):
                    return self.error(400)
                records = self.snapshot()
                start = (page - 1) * per_page
                values = records[start:start + per_page]
                status, response = self.result(values)
                response['result_info'].update(page=page, per_page=per_page, count=len(values),
                    total_count=len(records), total_pages=max(1, (len(records) + per_page - 1) // per_page))
                return status, response
            if not path.startswith(prefix + '/dns_records'):
                return self.error(404)
            self.mutations.append(dict(method=method, path=path, payload=copy.deepcopy(payload)))
            saved, previous_id = copy.deepcopy(self.records), self.next_id
            try:
                if method == 'POST' and path == prefix + '/dns_records/batch':
                    if not isinstance(payload, dict) or set(payload) - {'posts', 'puts', 'patches', 'deletes'}:
                        raise ValueError('unsupported batch')
                    if sum(len(values) for values in payload.values()) > 200:
                        raise ValueError('unbounded batch')
                    result = {}
                    for group, operation in [('deletes', 'DELETE'), ('puts', 'PUT'), ('patches', 'PUT'), ('posts', 'POST')]:
                        result[group] = [self.change(operation, item, item.get('id')) for item in payload.get(group, [])]
                    return self.result(result)
                if method == 'POST' and path == prefix + '/dns_records':
                    return self.result(self.change(method, payload))
                match = re.fullmatch(re.escape(prefix) + r'/dns_records/([0-9a-f]{32})', path)
                if match and method in {'PUT', 'PATCH', 'DELETE'}:
                    return self.result(self.change('PUT' if method == 'PATCH' else method, payload, match[1]))
                raise ValueError('unsupported request')
            except (ValueError, TypeError, KeyError, AttributeError):
                self.records, self.next_id = saved, previous_id
                return self.error(400)


class FakeCloudflareServer:
    def __init__(self, model, address='127.0.0.1'):
        if address != '127.0.0.1' and (address != '192.0.2.1' or os.environ.get('GITHUB_ACTIONS') != 'true'
                                      or os.environ.get('BAREPLANE_DISPOSABLE_VM') != '1'):
            raise ValueError('Non-loopback fake APIs are restricted to the disposable GitHub VM bridge')

        class Handler(BaseHTTPRequestHandler):
            def setup(self):
                super().setup()
                self.connection.settimeout(5)

            def log_message(self, *args):
                pass  # Never log authorization headers, bodies, or tokens.

            def respond(self):
                try:
                    size = int(self.headers.get('Content-Length', '0'))
                    if not 0 <= size <= 65536:
                        raise ValueError('oversized request')
                    body = json.loads(self.rfile.read(size)) if size else None
                    status, response = model.handle(self.command, self.path, self.headers.get('Authorization'), body)
                except (ValueError, UnicodeError):
                    status, response = model.error(400)
                data = json.dumps(response).encode()
                self.send_response(status)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            do_GET = do_POST = do_PUT = do_PATCH = do_DELETE = respond

        self.server = ThreadingHTTPServer((address, 0), Handler)
        self.server.daemon_threads = True
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.url = f'http://{address}:{self.server.server_port}/client/v4/'

    def __enter__(self):
        self.thread.start()
        return self

    def __exit__(self, *args):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)
