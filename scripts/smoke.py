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
    snapshot = s.directory / 'data' / 'strata.json'
    original = snapshot.read_bytes()
    damaged = json.loads(original)
    damaged['digest'] = '0' * 64
    snapshot.write_text(json.dumps(damaged))
    attempt = subprocess.run([str(s.binary), '-addr', '127.0.0.1:0', '-data', str(s.directory/'data')], capture_output=True, timeout=10)
    assert attempt.returncode != 0 and b'checksum mismatch' in attempt.stderr
    snapshot.write_bytes(original)
    s.start()
    assert s.call('GET', f'/api/v1/profiles/{p["id"]}') == p


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


def legacy_cmp_id(body):
    # 复刻升级前 Go 端的编号推导：sha256(json({Algorithm, Request}))，载荷不出现多余字段。
    payload = json.dumps({'Algorithm': 'interval-v1', 'Request': body}, separators=(',', ':'),
                         ensure_ascii=False).encode()
    return 'cmp_' + hashlib.sha256(payload).hexdigest()[:32]


def compare(s):
    a, b = sealed(s, '西侧剖面'), sealed(s, '东侧剖面', 6000)
    request = dict(left=dict(id=a['id'], version=3), right=dict(id=b['id'], version=3), offset_mm=0)
    # 新发起的对比默认使用当前算法版本。
    result = s.call('POST', '/api/v1/comparisons', request, 201)
    assert result['algorithm'] == 'interval-v2'
    assert result['overlap_mm'] == 10000 and result['equal_mm'] == 8000 and result['similarity'] == .8
    assert s.call('POST', '/api/v1/comparisons', request) == result
    rows = list(csv.DictReader(io.StringIO(s.call('GET', f'/api/v1/comparisons/{result["id"]}/csv', raw=True))))
    assert len(rows) == 3 and sum(int(r['thickness_mm']) for r in rows) == 10000
    algorithms = s.call('GET', '/api/v1/comparison-algorithms')
    assert algorithms['current'] == 'interval-v2'
    assert {x['algorithm'] for x in algorithms['items']} == {'interval-v1', 'interval-v2'}
    proposal = s.call('POST', '/api/v1/comparison-offsets', dict(left=request['left'], right=request['right']))
    assert proposal['comparison']['offset_mm'] == -2000 and not proposal['ambiguous']
    aligned = s.call('POST', '/api/v1/comparisons', proposal['comparison'], 201)
    assert aligned['algorithm'] == 'interval-v2' and aligned['similarity'] == 1 and aligned['overlap_mm'] == 8000
    s.call('POST', '/api/v1/comparisons', {**request, 'offset_mm': 10000}, 409)
    s.call('POST', '/api/v1/comparisons', {**request, 'left': dict(id=a['id'], version=2)}, 409)
    c = state(s, replace(s, create(s, '待识别岩性'), [dict(top_mm=0, bottom_mm=10000, rock='unknown')]), 'seal')

    # 旧算法版本仍可显式使用；编号推导与升级前逐字节一致，旧编号不会失效。
    v1_request = {**request, 'algorithm': 'interval-v1'}
    v1 = s.call('POST', '/api/v1/comparisons', v1_request, 201)
    assert v1['id'] == legacy_cmp_id(request) and v1['algorithm'] == 'interval-v1'
    assert s.call('POST', '/api/v1/comparisons', v1_request) == v1
    # 同一输入在当前版本下与旧版本并存，互不覆盖。
    v2 = s.call('POST', '/api/v1/comparisons', {**request, 'algorithm': 'interval-v2'}, 200)
    assert v2['id'] != v1['id'] and s.call('GET', f'/api/v1/comparisons/{v1["id"]}')['algorithm'] == 'interval-v1'
    s.call('POST', '/api/v1/comparisons', {**request, 'algorithm': 'interval-v9'}, 422)

    # v1 与 v2 的差异只在单侧未知区间：v1 记 unknown 不进分母，v2 记 different 进分母。
    unknown_v1 = s.call('POST', '/api/v1/comparisons',
                        {**v1_request, 'right': dict(id=c['id'], version=3)}, 201)
    assert unknown_v1['known_mm'] == 0 and unknown_v1['similarity'] is None
    unknown_v2 = s.call('POST', '/api/v1/comparisons',
                        dict(left=request['left'], right=dict(id=c['id'], version=3), offset_mm=0), 201)
    assert unknown_v2['known_mm'] == 10000 and unknown_v2['equal_mm'] == 0 and unknown_v2['similarity'] == 0
    assert {seg['relation'] for seg in unknown_v1['segments']} == {'unknown'}
    assert {seg['relation'] for seg in unknown_v2['segments']} == {'different'}

    # 按算法版本筛选；未知版本被拒绝。
    page_v1 = s.call('GET', '/api/v1/comparisons?algorithm=interval-v1')
    assert page_v1['total'] == 2 and {x['algorithm'] for x in page_v1['items']} == {'interval-v1'}
    page_v2 = s.call('GET', '/api/v1/comparisons?algorithm=interval-v2')
    assert page_v2['total'] == 3 and {x['algorithm'] for x in page_v2['items']} == {'interval-v2'}
    s.call('GET', '/api/v1/comparisons?algorithm=interval-v9', expected=422)
    assert s.call('GET', f"/api/v1/comparisons?profile_id={a['id']}&algorithm=interval-v1")['total'] == 2

    # 重算前先说明两个版本会得出什么差异；预览不落盘。
    diff = s.call('GET', f'/api/v1/comparisons/{unknown_v1["id"]}/algorithm-diff?algorithm=interval-v2')
    assert diff['from_algorithm'] == 'interval-v1' and diff['to_algorithm'] == 'interval-v2'
    assert not diff['identical'] and diff['to_metrics']['known_mm'] == 10000
    assert len(diff['changes']) == 2
    assert {c['from_relation'] for c in diff['changes']} == {'unknown'}
    assert {c['to_relation'] for c in diff['changes']} == {'different'}
    assert sum(c['thickness_mm'] for c in diff['changes']) == 10000
    assert diff['summary']
    assert s.call('GET', '/api/v1/comparisons?algorithm=interval-v1')['total'] == 2
    # 对两侧岩性都已知的结果，两版本结论一致。
    same = s.call('GET', f'/api/v1/comparisons/{v1["id"]}/algorithm-diff?algorithm=interval-v2')
    assert same['identical'] and same['changes'] == []
    s.call('GET', f'/api/v1/comparisons/{v1["id"]}/algorithm-diff?algorithm=interval-v1', expected=409)

    # 目标版本尚未保存时，重算生成新结果（201）并保留原结果；偏移 -4000 只有 v1。
    fresh_v1 = s.call('POST', '/api/v1/comparisons', {**v1_request, 'offset_mm': -4000}, 201)
    outcome = s.call('POST', f'/api/v1/comparisons/{fresh_v1["id"]}/recompute', dict(algorithm='interval-v2'), 201)
    assert outcome['source']['id'] == fresh_v1['id'] and not outcome['reused']
    fresh_v2 = outcome['result']
    assert fresh_v2['algorithm'] == 'interval-v2' and fresh_v2['id'] != fresh_v1['id']
    assert s.call('GET', f'/api/v1/comparisons/{fresh_v1["id"]}') == fresh_v1
    assert s.call('GET', f'/api/v1/comparisons/{fresh_v2["id"]}')['algorithm'] == 'interval-v2'
    # 目标版本已存在时复用（200），同样保留原结果。
    reused = s.call('POST', f'/api/v1/comparisons/{unknown_v1["id"]}/recompute', dict(algorithm='interval-v2'), 200)
    assert reused['reused'] and reused['result']['id'] == unknown_v2['id']
    assert s.call('GET', f'/api/v1/comparisons/{unknown_v1["id"]}') == unknown_v1
    s.call('POST', f'/api/v1/comparisons/{unknown_v2["id"]}/recompute', dict(algorithm='interval-v2'), 409)

    state(s, a, 'reopen')
    assert s.call('POST', '/api/v1/comparisons', request) == result
    s.stop()
    s.start()
    # 重启后两个版本的原编号都仍能找到，启动校验按各自算法版本通过。
    assert s.call('GET', f'/api/v1/comparisons/{result["id"]}') == result
    assert s.call('GET', f'/api/v1/comparisons/{v1["id"]}') == v1
    assert s.call('GET', f'/api/v1/comparisons/{unknown_v1["id"]}') == unknown_v1
    list(csv.DictReader(io.StringIO(s.call('GET', f'/api/v1/comparisons/{unknown_v1["id"]}/csv', raw=True))))


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
