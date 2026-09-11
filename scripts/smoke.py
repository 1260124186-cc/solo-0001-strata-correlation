#!/usr/bin/env python3
"""Run bounded HTTP checks in a disposable data directory using the real server."""
import argparse
import concurrent.futures
import csv
import hashlib
import io
import json
import os
from pathlib import Path
import selectors
import signal
import subprocess
import tempfile
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parent.parent


class Server:
    def __init__(self, directory):
        self.directory = Path(directory)
        self.binary = self.directory / 'stratad'
        args = ['go', 'build']
        if os.environ.get('STRATA_SMOKE_RACE') == '1':
            args.append('-race')
        subprocess.run(args + ['-o', str(self.binary), './cmd/stratad'], cwd=ROOT, check=True, timeout=60)
        self.process = None
        self.log = open(self.directory / 'server.log', 'w+')
        try:
            self.start()
        except BaseException:
            if self.process is not None and self.process.poll() is None:
                self.process.kill()
                self.process.wait(timeout=5)
            self.log.close()
            raise

    def start(self):
        self.process = subprocess.Popen(
            [str(self.binary), '-addr', '127.0.0.1:0', '-data', str(self.directory / 'data')],
            stdout=subprocess.PIPE, stderr=self.log, text=True,
        )
        with selectors.DefaultSelector() as selector:
            selector.register(self.process.stdout, selectors.EVENT_READ)
            if not selector.select(timeout=10):
                raise RuntimeError('server did not become ready')
            line = self.process.stdout.readline().strip()
        if not line.startswith('STRATA_LISTEN='):
            raise RuntimeError('invalid ready output: ' + line)
        self.url = 'http://' + line.partition('=')[2]
        self.call('GET', '/healthz')

    def stop(self):
        if self.process is None:
            return
        if self.process.poll() is None:
            self.process.send_signal(signal.SIGTERM)
            try:
                self.process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=5)
                raise RuntimeError('server did not shut down')
        if self.process.returncode != 0:
            raise RuntimeError('server failed: ' + str(self.process.returncode))
        self.process.stdout.close()
        self.process = None

    def close(self):
        try:
            self.stop()
        finally:
            if self.process and self.process.poll() is None:
                self.process.kill()
                self.process.wait(timeout=5)
            self.log.flush()
            self.log.seek(0)
            content = self.log.read()
            self.log.close()
            if 'DATA RACE' in content:
                raise RuntimeError('race detector found a race')

    def call(self, method, path, body=None, expected=200, raw=False):
        data = None if body is None else json.dumps(body, ensure_ascii=False).encode()
        request = urllib.request.Request(self.url + path, data=data, method=method,
                                         headers={'Content-Type': 'application/json'})
        try:
            response = urllib.request.urlopen(request, timeout=10)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            payload = response.read()
            if response.status != expected:
                raise RuntimeError(f'{method} {path}: expected {expected}, got {response.status}: {payload.decode()}')
        return payload.decode() if raw else json.loads(payload)


def create(s, name='北坡剖面', site='赤石岭', depth=10000):
    return s.call('POST', '/api/v1/profiles', dict(name=name, site=site, depth_mm=depth, note='岩层编录'), 201)


def layers(boundary=4000):
    return [dict(top_mm=0, bottom_mm=boundary, rock='sandstone', marker=''),
            dict(top_mm=boundary, bottom_mm=10000, rock='mudstone', marker='凝灰标志')]


def replace(s, p, values, expected=200):
    return s.call('PUT', f'/api/v1/profiles/{p["id"]}/layers',
                  dict(expected_version=p['version'], layers=values, reason='补充分层记录'), expected)


def state(s, p, action, expected=200):
    return s.call('POST', f'/api/v1/profiles/{p["id"]}/{action}',
                  dict(expected_version=p['version'], reason='完成岩层核对'), expected)


def sealed(s, name, boundary=4000):
    return state(s, replace(s, create(s, name), layers(boundary)), 'seal')


def record(s):
    p = create(s)
    assert p['version'] == 1 and p['layers'] == []
    s.call('POST', '/api/v1/profiles', dict(name='空', site='地点', depth_mm=0), 422)
    s.call('POST', '/api/v1/profiles', dict(name='空', site='地点', depth_mm=10, extra=True), 422)
    p = replace(s, p, [dict(top_mm=1000, bottom_mm=9000, rock='sandstone')])
    coverage = s.call('GET', f'/api/v1/profiles/{p["id"]}/coverage')['coverage']
    assert coverage['gaps'] == [dict(top_mm=0, bottom_mm=1000), dict(top_mm=9000, bottom_mm=10000)]
    point = s.call('GET', f'/api/v1/profiles/{p["id"]}/at?depth_mm=500')
    assert point['layer'] is None and point['gap']['bottom_mm'] == 1000
    bad = [dict(top_mm=0, bottom_mm=6000, rock='shale'), dict(top_mm=5000, bottom_mm=10000, rock='mudstone')]
    replace(s, p, bad, 422)
    current = s.call('GET', f'/api/v1/profiles/{p["id"]}')
    assert current == p
    state(s, p, 'seal', 409)
    p = replace(s, p, list(reversed(layers())))
    assert p['layers'][0]['top_mm'] == 0
    point = s.call('GET', f'/api/v1/profiles/{p["id"]}/at?depth_mm=4000')
    assert point['layer']['rock'] == 'mudstone' and point['distance_from_top_mm'] == 0
    s.call('GET', f'/api/v1/profiles/{p["id"]}/at?depth_mm=10000', expected=422)
    patched = s.call('PATCH', f'/api/v1/profiles/{p["id"]}',
                     dict(expected_version=p['version'], metadata=dict(note='局部修订说明'), reason='补记说明'))
    assert patched['version'] == p['version'] + 1
    assert patched['name'] == p['name'] and patched['site'] == p['site']
    assert patched['depth_mm'] == p['depth_mm'] and patched['layers'] == p['layers']
    assert patched['note'] == '局部修订说明'
    event = s.call('GET', f'/api/v1/profiles/{p["id"]}/revisions/{patched["version"]}')['event']
    assert event['action'] == 'metadata' and event['changes'] == [
        dict(field='note', before='岩层编录', after='局部修订说明')]
    cleared = s.call('PATCH', f'/api/v1/profiles/{p["id"]}',
                     dict(expected_version=patched['version'], metadata=dict(note=''), reason='清空说明'))
    assert cleared['version'] == patched['version'] + 1 and cleared['note'] == ''
    for bad in [
        dict(expected_version=cleared['version'], metadata=dict(), reason='空补丁'),
        dict(expected_version=cleared['version'], metadata=dict(name='  '), reason='清空名称'),
        dict(expected_version=cleared['version'], metadata=dict(site=''), reason='清空地点'),
        dict(expected_version=cleared['version'], metadata=dict(note=None), reason='空值说明'),
        dict(expected_version=cleared['version'], reason='缺少元数据'),
        dict(expected_version=cleared['version'], metadata=dict(note='x', extra=1), reason='未知字段'),
        dict(expected_version=cleared['version'], metadata=dict(note='x'), reason='',),
    ]:
        s.call('PATCH', f'/api/v1/profiles/{p["id"]}', bad, 422)
    s.call('PATCH', f'/api/v1/profiles/{p["id"]}',
           dict(expected_version=cleared['version'], metadata=dict(depth_mm=3000), reason='收紧深度'), 422)
    body = dict(expected_version=cleared['version'],
                metadata=dict(name=cleared['name'], site=cleared['site'], depth_mm=3000), reason='调整深度')
    s.call('PUT', f'/api/v1/profiles/{p["id"]}', body, 422)
    s.stop()
    s.start()
    assert s.call('GET', f'/api/v1/profiles/{p["id"]}') == cleared
    s.process.kill()
    s.process.wait(timeout=5)
    s.process.stdout.close()
    s.process = None
    s.start()
    assert s.call('GET', f'/api/v1/profiles/{p["id"]}') == cleared
    s.stop()
    snapshot = s.directory / 'data' / 'strata.json'
    original = snapshot.read_bytes()
    damaged = json.loads(original)
    damaged['digest'] = '0' * 64
    snapshot.write_text(json.dumps(damaged))
    attempt = subprocess.run([str(s.binary), '-addr', '127.0.0.1:0', '-data', str(s.directory/'data')], capture_output=True, timeout=10)
    assert attempt.returncode != 0 and b'checksum mismatch' in attempt.stderr
    snapshot.write_bytes(original)
    s.start()
    assert s.call('GET', f'/api/v1/profiles/{p["id"]}') == cleared
    s.stop()

    def rewrap(mutate):
        envelope = json.loads(original)
        start = original.index(b'{', original.index(b'"data"'))
        depth, in_string, escaped, end = 0, False, False, -1
        for i in range(start, len(original)):
            c = original[i]
            if in_string:
                if escaped:
                    escaped = False
                elif c == ord('\\'):
                    escaped = True
                elif c == ord('"'):
                    in_string = False
            elif c == ord('"'):
                in_string = True
            elif c == ord('{'):
                depth += 1
            elif c == ord('}'):
                depth -= 1
                if depth == 0:
                    end = i + 1
                    break
        data_text = original[start:end].decode()
        data = json.loads(data_text)
        mutate(data)
        data_text = json.dumps(data, ensure_ascii=False)
        digest = hashlib.sha256(data_text.encode()).hexdigest()
        snapshot.write_text('{"digest":' + json.dumps(digest) + ',"data":' + data_text + '}')

    def strip_changes(data):
        for revisions in data['histories'].values():
            for revision in revisions:
                revision['event'].pop('changes', None)
    rewrap(strip_changes)
    s.start()
    assert s.call('GET', f'/api/v1/profiles/{p["id"]}') == cleared
    s.stop()

    def lie_changes(data):
        strip_changes(data)
        for revisions in data['histories'].values():
            for revision in revisions:
                if revision['event']['action'] == 'metadata':
                    revision['event']['changes'] = [
                        dict(field='note', before='不存在的旧值', after='')]
    rewrap(lie_changes)
    attempt = subprocess.run([str(s.binary), '-addr', '127.0.0.1:0', '-data', str(s.directory/'data')], capture_output=True, timeout=10)
    assert attempt.returncode != 0 and b'changes do not match' in attempt.stderr
    snapshot.write_bytes(original)
    s.start()
    assert s.call('GET', f'/api/v1/profiles/{p["id"]}') == cleared


def seal(s):
    p = sealed(s, '锁定剖面')
    replace(s, p, layers(), 409)
    locked_patch = s.call('PATCH', f'/api/v1/profiles/{p["id"]}',
                          dict(expected_version=3, metadata=dict(note='锁定后改说明'), reason='绕过尝试'), 409)
    assert locked_patch['error']['code'] == 'conflict' and 'changed_fields' not in locked_patch['error']
    locked = s.call('GET', f'/api/v1/profiles/{p["id"]}/revisions/3')
    p = state(s, p, 'reopen')
    state(s, p, 'reopen', 409)
    def edit(name):
        body = dict(expected_version=p['version'], metadata=dict(name=name, site=p['site'], depth_mm=p['depth_mm']), reason='修订名称')
        request = urllib.request.Request(s.url+f'/api/v1/profiles/{p["id"]}', method='PUT',
                    data=json.dumps(body).encode(), headers={'Content-Type': 'application/json'})
        try:
            response = urllib.request.urlopen(request, timeout=10)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            response.read()
            return response.status
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        codes = list(pool.map(edit, ['东岭剖面', '西岭剖面']))
    assert sorted(codes) == [200, 409], codes
    current = s.call('GET', f'/api/v1/profiles/{p["id"]}')
    assert current['version'] == 5
    assert current['name'] in ('东岭剖面', '西岭剖面')
    def patch_note(note):
        request = urllib.request.Request(
            s.url+f'/api/v1/profiles/{p["id"]}', method='PATCH',
            data=json.dumps(dict(expected_version=5, metadata=dict(note=note), reason='补记说明')).encode(),
            headers={'Content-Type': 'application/json'})
        try:
            response = urllib.request.urlopen(request, timeout=10)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            payload = json.loads(response.read())
        assert response.status in (200, 409), response.status
        return payload
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        results = list(pool.map(patch_note, ['南翼补勘说明', '北翼补勘说明']))
    winner = next(r for r in results if 'version' in r)
    loser = next(r for r in results if 'error' in r)
    assert winner['version'] == 6 and winner['note'] in ('南翼补勘说明', '北翼补勘说明')
    error = loser['error']
    assert error['code'] == 'version_conflict' and error['expected_version'] == 5 and error['current_version'] == 6
    assert error['changed_fields'] == [dict(field='note', before='', after=winner['note'])]
    event = s.call('GET', f'/api/v1/profiles/{p["id"]}/revisions/6')['event']
    assert event['changes'] == [dict(field='note', before='', after=winner['note'])]
    stale = s.call('PATCH', f'/api/v1/profiles/{p["id"]}',
                   dict(expected_version=4, metadata=dict(note='落后两版的说明'), reason='过期提交'), 409)['error']
    assert stale['expected_version'] == 4 and stale['current_version'] == 6
    assert {f['field'] for f in stale['changed_fields']} == {'name', 'note'}
    stale_sealed = s.call('PATCH', f'/api/v1/profiles/{p["id"]}',
                          dict(expected_version=3, metadata=dict(note='从锁定版起算'), reason='过期提交'), 409)['error']
    assert {f['field'] for f in stale_sealed['changed_fields']} == {'state', 'name', 'note'}
    state_change = next(f for f in stale_sealed['changed_fields'] if f['field'] == 'state')
    assert state_change['before'] == 'sealed' and state_change['after'] == 'draft'
    s.call('PATCH', f'/api/v1/profiles/{p["id"]}',
           dict(expected_version=99, metadata=dict(note='未来版本'), reason='越界提交'), 409)
    assert s.call('GET', f'/api/v1/profiles/{p["id"]}/revisions/3') == locked
    history = s.call('GET', f'/api/v1/profiles/{p["id"]}/history?offset=2&limit=2')
    assert history['total'] == 6 and [x['action'] for x in history['items']] == ['seal', 'reopen']
    diff = s.call('GET', f'/api/v1/profiles/{p["id"]}/diff?from=3&to=5')
    assert {f['field'] for f in diff['fields']} >= {'state', 'name'}
    assert diff['layers'] == []
    attempt = subprocess.run([str(s.binary), '-addr', '127.0.0.1:0', '-data', str(s.directory/'data')], capture_output=True, timeout=10)
    assert attempt.returncode != 0 and b'already in use' in attempt.stderr
    s.stop()
    s.start()
    assert s.call('GET', f'/api/v1/profiles/{p["id"]}') == winner


def compare(s):
    a, b = sealed(s, '西侧剖面'), sealed(s, '东侧剖面', 6000)
    request = dict(left=dict(id=a['id'], version=3), right=dict(id=b['id'], version=3), offset_mm=0)
    result = s.call('POST', '/api/v1/comparisons', request, 201)
    assert result['overlap_mm'] == 10000 and result['equal_mm'] == 8000 and result['similarity'] == .8
    assert s.call('POST', '/api/v1/comparisons', request) == result
    rows = list(csv.DictReader(io.StringIO(s.call('GET', f'/api/v1/comparisons/{result["id"]}/csv', raw=True))))
    assert len(rows) == 3 and sum(int(r['thickness_mm']) for r in rows) == 10000
    proposal = s.call('POST', '/api/v1/comparison-offsets', dict(left=request['left'], right=request['right']))
    assert proposal['comparison']['offset_mm'] == -2000 and not proposal['ambiguous']
    aligned = s.call('POST', '/api/v1/comparisons', proposal['comparison'], 201)
    assert aligned['similarity'] == 1 and aligned['overlap_mm'] == 8000
    s.call('POST', '/api/v1/comparisons', {**request, 'offset_mm': 10000}, 409)
    s.call('POST', '/api/v1/comparisons', {**request, 'left': dict(id=a['id'], version=2)}, 409)
    c = state(s, replace(s, create(s, '待识别岩性'), [dict(top_mm=0, bottom_mm=10000, rock='unknown')]), 'seal')
    unknown = s.call('POST', '/api/v1/comparisons', {**request, 'right': dict(id=c['id'], version=3)}, 201)
    assert unknown['known_mm'] == 0 and unknown['similarity'] is None
    state(s, a, 'reopen')
    assert s.call('POST', '/api/v1/comparisons', request) == result
    s.stop()
    s.start()
    assert s.call('GET', f'/api/v1/comparisons/{result["id"]}') == result


def browse(s):
    a = sealed(s, '赤石北剖面')
    b = create(s, '赤石南剖面')
    create(s, '远山剖面', '青岩岭')
    q = urllib.parse.urlencode(dict(q='赤石', site='赤石岭', state='draft', limit=1))
    page = s.call('GET', '/api/v1/profiles?'+q)
    assert page['total'] == 1 and page['items'][0]['id'] == b['id']
    page = s.call('GET', '/api/v1/profiles?rock=mudstone')
    assert page['total'] == 1 and page['items'][0]['id'] == a['id']
    assert s.call('GET', '/api/v1/profiles?offset=100')['items'] == []
    for query in ['limit=0', 'limit=no', 'limit=1&limit=2', 'state=absent', 'rock=absent', 'extra=1']:
        s.call('GET', '/api/v1/profiles?'+query, expected=422)
    assert len(s.call('GET', '/api/v1/rocks')['items']) == 6
    s.call('GET', '/api/v1/profiles/prf_'+'0'*32, expected=404)
    s.call('GET', f'/api/v1/profiles/{a["id"]}/revisions/500', expected=404)
    assert s.call('GET', f'/api/v1/comparisons?profile_id={a["id"]}')['items'] == []
    diff = s.call('GET', f'/api/v1/profiles/{a["id"]}/diff?from=1&to=3')
    assert len(diff['layers']) == 2


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('workflow', choices=['record', 'seal', 'compare', 'browse', 'all'])
    args = parser.parse_args()
    names = ['record', 'seal', 'compare', 'browse'] if args.workflow == 'all' else [args.workflow]
    for name in names:
        with tempfile.TemporaryDirectory(prefix='strata-smoke-') as directory:
            server = Server(directory)
            try:
                globals()[name](server)
            finally:
                server.close()
            print(f'{name}: HTTP workflow passed')


if __name__ == '__main__':
    main()
