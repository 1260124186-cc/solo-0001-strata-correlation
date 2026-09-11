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


def line(s):
    # 三个已锁定剖面作为测线位置；A、C 刻意放在同一距离制造并列。
    a = sealed(s, '测线甲剖面')
    b = sealed(s, '测线乙剖面')
    c = sealed(s, '测线丙剖面')
    draft_d = create(s, '未锁定剖面')

    def station(p, chainage, version=3):
        return dict(profile_id=p['id'], version=version, chainage_mm=chainage)

    # 草稿可以为空；绑定非锁定版本、缺失版本、同位置重复都要被明确拒绝。
    empty = s.call('POST', '/api/v1/lines', dict(name='赤石测线', note='沿山脚', stations=[]), 201)
    assert empty['state'] == 'draft' and empty['stations'] == [] and empty['revision'] == 1
    s.call('POST', '/api/v1/lines', dict(name='坏引用', stations=[station(draft_d, 0, version=1)]), 409)
    s.call('POST', '/api/v1/lines',
           dict(name='缺版本', stations=[dict(profile_id=a['id'], version=99, chainage_mm=0)]), 422)
    s.call('POST', '/api/v1/lines',
           dict(name='重复位置', stations=[station(a, 0), station(a, 100)]), 409)
    s.call('POST', '/api/v1/lines', dict(name=''), 422)
    s.call('POST', '/api/v1/lines', dict(name='多余字段', stations=[], extra=True), 422)

    # 同距离允许并列（不同剖面）；默认并列先后按 profile_id，且必须稳定。
    body = dict(name='赤石主测线', stations=[station(a, 5000), station(c, 5000), station(b, 12000)])
    ln = s.call('POST', '/api/v1/lines', body, 201)
    lid = ln['id']
    ids = [st['profile_id'] for st in ln['stations']]
    assert ids == sorted([a['id'], c['id']]) + [b['id']], ids
    tied = [st for st in ln['stations'] if st['chainage_mm'] == 5000]
    assert [st['order'] for st in tied] == [1, 2]
    assert [st['tie_group_index'] for st in tied] == [1, 2]
    assert all(st['tie_group_size'] == 2 and st['tied'] for st in tied)
    assert ln['stations'][2]['tied'] is False and ln['stations'][2]['tie_group_size'] == 1
    assert ln['revision'] == 1

    # 稳定性不依赖内部存储顺序：调整输入顺序重存，读回的相邻顺序必须一致。
    shuffled = dict(name='赤石主测线', stations=[station(b, 12000), station(c, 5000), station(a, 5000)])
    s.call('PUT', f'/api/v1/lines/{lid}/stations',
           dict(expected_revision=1, reason='换序录入', stations=shuffled['stations']), 200)
    again = s.call('GET', f'/api/v1/lines/{lid}')
    assert [st['profile_id'] for st in again['stations']] == ids
    assert again['revision'] == 2

    # 显式调整同距离并列先后：只能在距离单调不减的前提下重排。
    swapped = [c['id'], a['id'], b['id']]
    s.call('POST', f'/api/v1/lines/{lid}/reorder',
           dict(expected_revision=2, reason='现场确认甲在前', order=swapped), 200)
    reordered = s.call('GET', f'/api/v1/lines/{lid}')
    assert [st['profile_id'] for st in reordered['stations']] == swapped
    first_two = reordered['stations'][:2]
    assert [st['tie_group_index'] for st in first_two] == [1, 2]
    assert reordered['revision'] == 3
    # 想借排序制造距离倒退必须被拒绝；缺漏、多余、未知编号也拒绝。
    s.call('POST', f'/api/v1/lines/{lid}/reorder',
           dict(expected_revision=3, order=[b['id'], a['id'], c['id']]), 409)
    s.call('POST', f'/api/v1/lines/{lid}/reorder',
           dict(expected_revision=3, order=[a['id'], c['id']]), 422)
    s.call('POST', f'/api/v1/lines/{lid}/reorder',
           dict(expected_revision=3, order=[a['id'], a['id'], c['id'], b['id']]), 422)
    # 乐观修订号：过期写入得到 409，状态不变。
    s.call('PUT', f'/api/v1/lines/{lid}/stations',
           dict(expected_revision=1, stations=shuffled['stations']), 409)

    # 定稿冻结：定稿后任何写操作都被拒绝，仍可按当时内容查询。
    final = s.call('POST', f'/api/v1/lines/{lid}/finalize',
                   dict(expected_revision=3, reason='现场测线定稿'), 200)
    assert final['state'] == 'finalized' and final['revision'] == 4 and final['finalized_at']
    frozen_order = [st['profile_id'] for st in final['stations']]
    frozen_versions = {st['profile_id']: st['version'] for st in final['stations']}
    s.call('PUT', f'/api/v1/lines/{lid}', dict(expected_revision=4, name='改名'), 409)
    s.call('POST', f'/api/v1/lines/{lid}/reorder',
           dict(expected_revision=4, order=swapped), 409)
    s.call('POST', f'/api/v1/lines/{lid}/finalize', dict(expected_revision=4, reason='x'), 409)
    # 不足两个位置不能定稿。
    s.call('POST', f'/api/v1/lines/{empty["id"]}/finalize',
           dict(expected_revision=1, reason='空线定稿'), 409)

    # 成员剖面产生新版本 / 重新打开：定稿引用不被替换，只提示 outdated。
    a = state(s, a, 'reopen')
    a = replace(s, a, layers(), expected=200)
    a = state(s, a, 'seal')
    view = s.call('GET', f'/api/v1/lines/{lid}')
    by_id = {st['profile_id']: st for st in view['stations']}
    assert by_id[a['id']]['version'] == frozen_versions[a['id']]
    assert by_id[a['id']]['outdated'] is True
    assert by_id[a['id']]['current_version'] == a['version']
    assert by_id[b['id']]['outdated'] is False
    assert [st['profile_id'] for st in view['stations']] == frozen_order

    # 需要新版本：显式 fork 出新草稿，旧定稿保持原样可查。
    fork = s.call('POST', f'/api/v1/lines/{lid}/fork', dict(name='赤石主测线-修订'), 201)
    assert fork['state'] == 'draft' and fork['source_id'] == lid and fork['revision'] == 1
    # fork 默认原样复制旧引用（不自动升级），用户再显式把甲位置换到新版本。
    fid = fork['id']
    new_stations = [
        dict(profile_id=c['id'], version=3, chainage_mm=5000),
        dict(profile_id=a['id'], version=a['version'], chainage_mm=5000),
        dict(profile_id=b['id'], version=3, chainage_mm=12000),
    ]
    s.call('PUT', f'/api/v1/lines/{fid}/stations',
           dict(expected_revision=1, reason='采用甲剖面新版本', stations=new_stations), 200)
    fork2 = s.call('POST', f'/api/v1/lines/{fid}/finalize',
                   dict(expected_revision=2, reason='新版定稿'), 200)
    fmap = {st['profile_id']: st['version'] for st in fork2['stations']}
    assert fmap[a['id']] == a['version']
    # 旧定稿仍按当时冻结内容查询。
    old = s.call('GET', f'/api/v1/lines/{lid}')
    assert old['state'] == 'finalized'
    assert {st['profile_id']: st['version'] for st in old['stations']} == frozen_versions
    assert [st['profile_id'] for st in old['stations']] == frozen_order

    # 列表筛选、分页与定稿/草稿计数。
    finals = s.call('GET', '/api/v1/lines?state=finalized')
    assert finals['total'] == 2
    drafts = s.call('GET', '/api/v1/lines?state=draft')
    assert drafts['total'] == 1 and drafts['items'][0]['id'] == empty['id']
    found = s.call('GET', '/api/v1/lines?q='+urllib.parse.quote('赤石主测线'))
    assert found['total'] >= 2
    for bad in ['state=absent', 'limit=0', 'extra=1', 'limit=1&limit=2']:
        s.call('GET', '/api/v1/lines?'+bad, expected=422)
    s.call('GET', '/api/v1/lines/line_'+'0'*32, expected=404)

    # 重启恢复：定稿顺序与冻结版本保持不变，且不依赖 map 存储顺序。
    s.stop()
    s.start()
    recovered = s.call('GET', f'/api/v1/lines/{lid}')
    assert recovered['state'] == 'finalized'
    assert [st['profile_id'] for st in recovered['stations']] == frozen_order
    assert {st['profile_id']: st['version'] for st in recovered['stations']} == frozen_versions
    assert recovered == old


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('workflow', choices=['record', 'seal', 'compare', 'browse', 'line', 'all'])
    args = parser.parse_args()
    names = ['record', 'seal', 'compare', 'browse', 'line'] if args.workflow == 'all' else [args.workflow]
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
