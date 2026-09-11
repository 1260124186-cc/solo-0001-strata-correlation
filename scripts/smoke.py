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
import shutil
import signal
import subprocess
import tempfile
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parent.parent


def build_binary(directory, source=ROOT):
    args = ['go', 'build']
    if os.environ.get('STRATA_SMOKE_RACE') == '1':
        args.append('-race')
    subprocess.run(args + ['-o', str(Path(directory) / 'stratad'), './cmd/stratad'],
                   cwd=source, check=True, timeout=120)


class Server:
    def __init__(self, directory, source=ROOT):
        self.directory = Path(directory)
        self.binary = self.directory / 'stratad'
        build_binary(self.directory, source)
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

    left_layers = [
        dict(top_mm=0, bottom_mm=2000, rock='sandstone', marker='标志A'),
        dict(top_mm=2000, bottom_mm=4000, rock='mudstone', marker='标志B'),
        dict(top_mm=4000, bottom_mm=6000, rock='sandstone', marker='标志C'),
        dict(top_mm=6000, bottom_mm=8000, rock='mudstone', marker='标志D'),
        dict(top_mm=8000, bottom_mm=10000, rock='sandstone'),
    ]
    right_layers = [
        dict(top_mm=0, bottom_mm=2000, rock='sandstone', marker='标志A'),
        dict(top_mm=2000, bottom_mm=5000, rock='mudstone', marker='标志B'),
        dict(top_mm=5000, bottom_mm=7000, rock='sandstone', marker='标志C'),
        dict(top_mm=7000, bottom_mm=9000, rock='mudstone', marker='标志D'),
        dict(top_mm=9000, bottom_mm=10000, rock='sandstone'),
    ]
    l = state(s, replace(s, create(s, '标志层左'), left_layers), 'seal')
    rr = state(s, replace(s, create(s, '标志层右'), right_layers), 'seal')
    suggest_body = dict(left=dict(id=l['id'], version=3), right=dict(id=rr['id'], version=3))
    full = s.call('POST', '/api/v1/comparison-offsets', suggest_body)
    assert full['comparison']['offset_mm'] == -500 and full['ambiguous']
    assert [e['marker'] for e in full['evidence']] == ['标志A', '标志B', '标志C', '标志D']
    assert full['excluded_markers'] == [] and full['missing_markers'] == []
    # 剔除名单：忽略大小写与首尾空白、顺序不影响身份；含找不到的名称。
    body = dict(suggest_body, exclude_markers=[' 标志c ', '不存在', '标志D'])
    pruned = s.call('POST', '/api/v1/comparison-offsets', body)
    assert pruned['comparison']['offset_mm'] == 0 and not pruned['ambiguous']
    assert pruned['comparison']['exclude_markers'] == ['不存在', '标志c', '标志d']
    assert [e['marker'] for e in pruned['evidence']] == ['标志A', '标志B']
    assert {e['marker'] for e in pruned['excluded_markers']} == {'标志C', '标志D'}
    assert pruned['missing_markers'] == ['不存在']
    pruned_cmp = s.call('POST', '/api/v1/comparisons', pruned['comparison'], 201)
    assert [m['name'] for m in pruned_cmp['markers']] == ['标志A', '标志B']
    assert {m['name'] for m in pruned_cmp['excluded_markers']} == {'标志C', '标志D'}
    assert pruned_cmp['missing_markers'] == ['不存在']
    # 建议展示的输入与正式对比完全一致：同对象复用同一结果。
    assert s.call('POST', '/api/v1/comparisons', pruned['comparison']) == pruned_cmp
    # 同输入、剔除名单仅大小写/空白/顺序不同，复用同一结果。
    shuffled = dict(suggest_body, offset_mm=0, exclude_markers=['标志D', '标志C', ' 不存在 '])
    assert s.call('POST', '/api/v1/comparisons', shuffled)['id'] == pruned_cmp['id']
    # 有剔除但正式对比不带名单：是另一个身份，不会被静默套用剔除。
    no_exclude = dict(suggest_body, offset_mm=0)
    different = s.call('POST', '/api/v1/comparisons', no_exclude, 201)
    assert different['id'] != pruned_cmp['id']
    assert [m['name'] for m in different['markers']] == ['标志A', '标志B', '标志C', '标志D']
    assert different['excluded_markers'] == [] and different['missing_markers'] == []
    # 剔除全部共同标志层：建议失败，不退回到全部标志层。
    all_excluded = dict(suggest_body, exclude_markers=['标志A', '标志B', '标志C', '标志D'])
    s.call('POST', '/api/v1/comparison-offsets', all_excluded, 409)
    # 剔除名单重复或空名称被拒绝。
    s.call('POST', '/api/v1/comparison-offsets', dict(suggest_body, exclude_markers=['标志A', '标志a']), 422)
    s.call('POST', '/api/v1/comparison-offsets', dict(suggest_body, exclude_markers=['  ']), 422)
    s.call('POST', '/api/v1/comparisons', {**all_excluded, 'offset_mm': 0}, 201)

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


def upgrade(directory):
    """Read a snapshot produced by the pre-upgrade build with the new binary.

    The pre-exclude-marker baseline (cf54fe1) writes comparison results
    without excluded_markers/missing_markers. The new server must start on
    that data, keep the original conclusions, and continue serving requests.
    """
    directory = Path(directory)
    baseline = 'cf54fe1'
    worktree = directory / 'oldsrc'
    if subprocess.run(['git', '-C', str(ROOT), 'cat-file', '-e', baseline+'^{commit}'],
                      capture_output=True).returncode != 0:
        print('upgrade: skipped (baseline commit not available)')
        return
    subprocess.run(['git', '-C', str(ROOT), 'worktree', 'add', '--detach', str(worktree), baseline],
                   check=True, capture_output=True, timeout=60)
    legacy_dir = directory / 'legacy'
    legacy_dir.mkdir()
    newdata = directory / 'newdata'
    try:
        old = Server(legacy_dir, source=worktree)
        try:
            a, b = sealed(old, '西侧剖面'), sealed(old, '东侧剖面', 6000)
            req = dict(left=dict(id=a['id'], version=3), right=dict(id=b['id'], version=3), offset_mm=0)
            legacy = old.call('POST', '/api/v1/comparisons', req, 201)
            assert legacy['overlap_mm'] == 10000 and legacy['similarity'] == .8
            aligned_req = old.call('POST', '/api/v1/comparison-offsets',
                                   dict(left=req['left'], right=req['right']))['comparison']
            legacy_aligned = old.call('POST', '/api/v1/comparisons', aligned_req, 201)
            assert legacy_aligned['similarity'] == 1 and legacy_aligned['overlap_mm'] == 8000
        finally:
            old.close()
        snapshot = legacy_dir / 'data' / 'strata.json'
        on_disk = json.loads(snapshot.read_text())['data']
        for result in on_disk['comparisons'].values():
            assert 'excluded_markers' not in result and 'missing_markers' not in result
            assert 'exclude_markers' not in result['request']

        shutil.copytree(legacy_dir / 'data', newdata / 'data')
        current = Server(newdata)
        try:
            # 旧结论原样可读，结果编号不变。
            fetched = current.call('GET', f'/api/v1/comparisons/{legacy["id"]}')
            for key in ('id', 'algorithm', 'segments', 'markers', 'overlap_mm', 'known_mm',
                        'equal_mm', 'similarity', 'request'):
                assert fetched[key] == legacy[key], key
            assert fetched['excluded_markers'] == [] and fetched['missing_markers'] == []
            aligned = current.call('GET', f'/api/v1/comparisons/{legacy_aligned["id"]}')
            assert aligned['similarity'] == 1 and aligned['overlap_mm'] == 8000
            # 同一输入仍复用旧结果，而不是另建。
            assert current.call('POST', '/api/v1/comparisons', req)['id'] == legacy['id']
            assert current.call('POST', '/api/v1/comparisons', aligned_req)['id'] == legacy_aligned['id']
            # 新能力在迁移后的快照上照常工作。
            current.call('POST', '/api/v1/comparison-offsets',
                         dict(left=req['left'], right=req['right'], exclude_markers=['凝灰标志']), 409)
            # 触发一次写入，迁移结果以新格式持久化；重启后结论不变。
            create(current, '升级后新建剖面')
            current.stop()
            current.start()
            assert current.call('GET', f'/api/v1/comparisons/{legacy["id"]}')['similarity'] == .8
        finally:
            current.close()
    finally:
        subprocess.run(['git', '-C', str(ROOT), 'worktree', 'remove', '--force', str(worktree)],
                       capture_output=True)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('workflow', choices=['record', 'seal', 'compare', 'browse', 'upgrade', 'all'])
    args = parser.parse_args()
    names = (['record', 'seal', 'compare', 'browse'] if args.workflow == 'all'
             else [args.workflow])
    for name in names:
        with tempfile.TemporaryDirectory(prefix='strata-smoke-') as directory:
            if name == 'upgrade':
                upgrade(directory)
                print('upgrade: legacy snapshot workflow passed')
                continue
            server = Server(directory)
            try:
                globals()[name](server)
            finally:
                server.close()
            print(f'{name}: HTTP workflow passed')


if __name__ == '__main__':
    main()
