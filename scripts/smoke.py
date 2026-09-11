#!/usr/bin/env python3
"""Run bounded HTTP checks in a disposable data directory using the real server."""
import argparse
import concurrent.futures
import csv
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
            # Loading and validating a near-limit shard can take longer than a
            # normal start under the race detector; the default stays tight.
            ready_timeout = int(os.environ.get('STRATA_SMOKE_READY_TIMEOUT', '10'))
            if not selector.select(timeout=ready_timeout):
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
    body = dict(expected_version=p['version'], metadata=dict(name=p['name'], site=p['site'], depth_mm=3000), reason='调整深度')
    s.call('PUT', f'/api/v1/profiles/{p["id"]}', body, 422)
    s.stop()
    s.start()
    assert s.call('GET', f'/api/v1/profiles/{p["id"]}') == p
    s.process.kill()
    s.process.wait(timeout=5)
    s.process.stdout.close()
    s.process = None
    s.start()
    assert s.call('GET', f'/api/v1/profiles/{p["id"]}') == p
    s.stop()
    shard_dir = s.directory / 'data' / 'shards'
    shard_files = sorted(shard_dir.glob('*.json'))
    assert shard_files and not (s.directory / 'data' / 'strata.json').exists()
    shard = shard_dir / f'{p["id"]}.json'
    original = shard.read_bytes()
    damaged = json.loads(original)
    damaged['digest'] = '0' * 64
    shard.write_text(json.dumps(damaged))
    attempt = subprocess.run([str(s.binary), '-addr', '127.0.0.1:0', '-data', str(s.directory/'data')], capture_output=True, timeout=10)
    assert attempt.returncode != 0 and b'checksum mismatch' in attempt.stderr
    shard.write_bytes(original)
    s.start()
    assert s.call('GET', f'/api/v1/profiles/{p["id"]}') == p
    leftover = list(shard_dir.glob('.strata-*'))
    assert not leftover, leftover


def seal(s):
    p = sealed(s, '锁定剖面')
    replace(s, p, layers(), 409)
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
    assert s.call('GET', f'/api/v1/profiles/{p["id"]}/revisions/3') == locked
    history = s.call('GET', f'/api/v1/profiles/{p["id"]}/history?offset=2&limit=2')
    assert history['total'] == 5 and [x['action'] for x in history['items']] == ['seal', 'reopen']
    diff = s.call('GET', f'/api/v1/profiles/{p["id"]}/diff?from=3&to=5')
    assert {f['field'] for f in diff['fields']} >= {'state', 'name'}
    assert diff['layers'] == []
    attempt = subprocess.run([str(s.binary), '-addr', '127.0.0.1:0', '-data', str(s.directory/'data')], capture_output=True, timeout=10)
    assert attempt.returncode != 0 and b'already in use' in attempt.stderr
    s.stop()
    s.start()
    assert s.call('GET', f'/api/v1/profiles/{p["id"]}') == current


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


def legacy_snapshot(directory):
    """Write a pre-sharding whole snapshot and return the expected profile."""
    data_dir = directory / 'data'
    data_dir.mkdir(parents=True, exist_ok=True)
    ts = '2026-09-11T00:00:00Z'
    profile = dict(
        id='prf_' + 'ab' * 16,
        name='旧快照剖面', site='赤石岭', note='迁移前数据', depth_mm=10000,
        layers=[
            dict(top_mm=0, bottom_mm=4000, rock='sandstone', description='', marker=''),
            dict(top_mm=4000, bottom_mm=10000, rock='mudstone', description='', marker='凝灰标志'),
        ],
        state='sealed', version=3, created_at=ts, updated_at=ts,
    )
    def event(action, version):
        return dict(action=action, reason='历史记录', version=version, at=ts)
    v1 = dict(profile, layers=[], state='draft', version=1)
    v2 = dict(profile, state='draft', version=2)
    payload = dict(schema=1, histories={profile['id']: [
        dict(profile=v1, event=event('create', 1)),
        dict(profile=v2, event=event('layers', 2)),
        dict(profile=profile, event=event('seal', 3)),
    ]}, comparisons={})
    raw = json.dumps(payload, ensure_ascii=False, separators=(',', ':')).encode()
    import hashlib
    # Keep the data bytes identical to the bytes the digest was taken over;
    # embedding them as a raw JSON fragment avoids any re-serialization drift.
    envelope = ('{"digest":"' + hashlib.sha256(raw).hexdigest() + '","data":' +
                raw.decode() + '}')
    (data_dir / 'strata.json').write_text(envelope)
    return profile


def migrate(s):
    # s was booted on an empty directory; stop it and replace the store with a
    # legacy whole snapshot before the next start triggers migration.
    s.stop()
    expected = legacy_snapshot(s.directory)
    snapshot = s.directory / 'data' / 'strata.json'
    assert snapshot.exists()
    s.start()
    assert not snapshot.exists(), 'legacy snapshot must be removed after migration'
    shards = sorted((s.directory / 'data' / 'shards').glob('*.json'))
    assert [p.name for p in shards] == [expected['id'] + '.json']
    assert s.call('GET', f'/api/v1/profiles/{expected["id"]}') == expected
    locked = s.call('GET', f'/api/v1/profiles/{expected["id"]}/revisions/3')
    assert locked['profile'] == expected
    s.stop()
    s.start()
    assert s.call('GET', f'/api/v1/profiles/{expected["id"]}') == expected


def status_call(s, method, path, body):
    """Like Server.call but returns (status, payload) without asserting."""
    data = None if body is None else json.dumps(body, ensure_ascii=False).encode()
    request = urllib.request.Request(s.url + path, data=data, method=method,
                                     headers={'Content-Type': 'application/json'})
    try:
        response = urllib.request.urlopen(request, timeout=30)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return response.status, response.read().decode()


def capacity(s):
    # 500 layers each with a 900-character description grow every sealed
    # revision by roughly 1.5 MiB; sealing and reopening records the full
    # layer set in two revisions per cycle. The 64 MiB shard limit must be hit
    # before the 500-version limit, and the oversized write must be refused.
    p = create(s, '大容量剖面', '赤石岭', 1000000)
    fill = []
    for i in range(500):
        top, bottom = i * 2000, (i + 1) * 2000
        fill.append(dict(top_mm=top, bottom_mm=bottom, rock='sandstone' if i % 2 else 'mudstone',
                         description='描' * 900, marker=''))
    rejected = None
    last = p
    for cycle in range(30):
        body = dict(expected_version=last['version'], layers=fill, reason='补充分层记录')
        status, payload = status_call(s, 'PUT', f'/api/v1/profiles/{p["id"]}/layers', body)
        if status == 409:
            rejected = ('layers', json.loads(payload))
            break
        assert status == 200, (status, payload)
        last = json.loads(payload)
        status, payload = status_call(s, 'POST', f'/api/v1/profiles/{p["id"]}/seal',
                                      dict(expected_version=last['version'], reason='完成核对'))
        if status == 409:
            rejected = ('seal', json.loads(payload))
            break
        assert status == 200, (status, payload)
        last = json.loads(payload)
        status, payload = status_call(s, 'POST', f'/api/v1/profiles/{p["id"]}/reopen',
                                      dict(expected_version=last['version'], reason='继续补录'))
        if status == 409:
            rejected = ('reopen', json.loads(payload))
            break
        assert status == 200, (status, payload)
        last = json.loads(payload)
    assert rejected is not None, 'never reached the shard capacity limit'
    assert '64 MiB' in rejected[1]['error']['detail'], rejected
    assert s.call('GET', '/healthz')['status'] == 'ready'

    # The refused revision never entered memory or disk.
    current = s.call('GET', f'/api/v1/profiles/{p["id"]}')
    assert current == last
    shard = s.directory / 'data' / 'shards' / f'{p["id"]}.json'
    persisted = shard.stat().st_size
    assert persisted <= 64 * 1024 * 1024, persisted
    # Replaying the exact oversized request is rejected the same way; the
    # refused (not durable) revision must not have mutated memory or disk.
    def attempt_overflow():
        if rejected[0] == 'layers':
            body = dict(expected_version=last['version'], layers=fill, reason='再次补录')
            return status_call(s, 'PUT', f'/api/v1/profiles/{p["id"]}/layers', body)
        action = 'seal' if rejected[0] == 'seal' else 'reopen'
        return status_call(s, 'POST', f'/api/v1/profiles/{p["id"]}/{action}',
                           dict(expected_version=last['version'], reason='再次尝试'))
    status, payload = attempt_overflow()
    assert status == 409 and '64 MiB' in json.loads(payload)['error']['detail'], (status, payload)
    assert s.call('GET', f'/api/v1/profiles/{p["id"]}') == current

    # Another profile remains fully writable: one oversized shard cannot lock
    # the whole directory.
    other = replace(s, create(s, '其他剖面'), layers())
    assert other['version'] == 2

    # Restart from the last accepted state: recovery must succeed despite the
    # store containing a shard just under the 64 MiB read-side limit.
    s.stop()
    s.start()
    assert s.call('GET', '/healthz')['status'] == 'ready'
    assert s.call('GET', f'/api/v1/profiles/{p["id"]}') == current
    assert s.call('GET', f'/api/v1/profiles/{other["id"]}') == other
    status, payload = attempt_overflow()
    assert status == 409 and '64 MiB' in json.loads(payload)['error']['detail'], (status, payload)
    assert not list(s.directory.glob('data/shards/.strata-*'))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('workflow', choices=['record', 'seal', 'compare', 'browse', 'migrate', 'capacity', 'all'])
    args = parser.parse_args()
    names = ['record', 'seal', 'compare', 'browse', 'migrate', 'capacity'] if args.workflow == 'all' else [args.workflow]
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
