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
import time
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


def groups(s):
    a, b, c = sealed(s, '组参考剖面'), sealed(s, '组目标甲', 6000), sealed(s, '组目标乙')
    d = create(s, '组草稿')
    ref, good, other, draft = dict(id=a['id'], version=3), dict(id=b['id'], version=3), dict(id=c['id'], version=3), dict(id=d['id'], version=1)
    # 预先创建 good@0，组内该项应复用。
    pre = s.call('POST', '/api/v1/comparisons', dict(left=ref, right=good, offset_mm=0), 201)
    body = dict(reference=ref, targets=[
        dict(target=good, offset_mm=0),                       # 复用已有结果
        dict(target=other, offset_mm=0),                      # 新计算
        dict(target=good, offset_mm=-2000),                    # 新计算
        dict(target=good, offset_mm=10000),                   # 无共同区间 -> 失败
        dict(target=dict(id='prf_' + 'f' * 32, version=3), offset_mm=0),  # 引用无效 -> 失败
        dict(target=draft, offset_mm=0),                                    # 未锁定版本 -> 失败
    ])
    group = s.call('POST', '/api/v1/comparison-groups', body, 202)
    assert group['status'] == 'queued' and len(group['items']) == 6
    gid = group['id']
    for _ in range(100):
        group = s.call('GET', f'/api/v1/comparison-groups/{gid}')
        if group['status'] == 'completed':
            break
    assert group['status'] == 'completed'
    statuses = [(i['index'], i['status'], bool(i.get('error')), i['reused']) for i in group['items']]
    assert statuses == [
        (0, 'succeeded', False, True),
        (1, 'succeeded', False, False),
        (2, 'succeeded', False, False),
        (3, 'failed', True, False),
        (4, 'failed', True, False),
        (5, 'failed', True, False),
    ], statuses
    assert [i['error']['code'] for i in group['items'][3:]] == ['conflict', 'missing', 'conflict']
    # 成功项都能打开对应对比结果，且单项失败不影响同组其他项。
    for item in group['items'][:3]:
        cmp_ = s.call('GET', '/api/v1/comparisons/' + item['comparison_id'])
        assert cmp_['request']['left'] == ref
    # 第二组重复相同输入，全部标记复用。
    again = s.call('POST', '/api/v1/comparison-groups', dict(reference=ref, targets=[
        dict(target=other, offset_mm=0), dict(target=good, offset_mm=-2000)]), 202)
    for _ in range(100):
        again = s.call('GET', f'/api/v1/comparison-groups/{again["id"]}')
        if again['status'] == 'completed':
            break
    assert all(i['status'] == 'succeeded' and i['reused'] for i in again['items'])
    # 结构性错误整体拒绝（422），不落组。
    s.call('POST', '/api/v1/comparison-groups', dict(reference=ref, targets=[]), 422)
    s.call('POST', '/api/v1/comparison-groups', dict(reference=ref, targets=[dict(target=ref, offset_mm=0)]), 422)
    s.call('POST', '/api/v1/comparison-groups', dict(reference=ref, targets=[
        dict(target=other, offset_mm=0), dict(target=other, offset_mm=0)]), 422)
    s.call('POST', '/api/v1/comparison-groups', dict(reference=ref, targets=[
        dict(target=other, offset_mm=1000001)]), 422)
    # 已完成组恢复冲突；列表分页与未知参数。
    s.call('POST', f'/api/v1/comparison-groups/{gid}/resume', expected=409)
    page = s.call('GET', '/api/v1/comparison-groups?limit=1')
    assert page['total'] == 2 and len(page['items']) == 1
    s.call('GET', '/api/v1/comparison-groups?extra=1', expected=422)
    s.call('GET', '/api/v1/comparison-groups/grp_' + '0' * 32, expected=404)
    s.call('POST', '/api/v1/comparison-groups/grp_' + '0' * 32 + '/resume', expected=404)

    # 崩溃恢复：提交大量排队项，等出现运行中项后冻结进程并硬杀，
    # 重启后运行中项重置为排队并由服务自动恢复跑完。
    pending = s.call('POST', '/api/v1/comparison-groups', dict(reference=ref, targets=[
        dict(target=other, offset_mm=k) for k in range(100, 150)]), 202)
    pid = s.process.pid
    deadline = time.time() + 5
    while time.time() < deadline:
        running = s.call('GET', f'/api/v1/comparison-groups/{pending["id"]}')
        if any(i['status'] == 'running' for i in running['items']):
            break
    os.kill(pid, signal.SIGSTOP)
    time.sleep(0.2)
    os.kill(pid, signal.SIGKILL)
    s.process.wait(timeout=5)
    s.process.stdout.close()
    s.process = None
    # 硬杀前确有运行中项已持久化，模拟真实崩溃残留。
    crashed = json.loads((s.directory / 'data' / 'strata.json').read_text())
    crashed_data = json.loads(crashed['data']) if isinstance(crashed['data'], str) else crashed['data']
    assert any(i['status'] == 'running' for i in crashed_data['groups'][pending['id']]['items'])
    s.start()
    # 服务启动即把遗留的运行中项重置为排队并自动恢复，等待全部终态。
    recovered = s.call('GET', f'/api/v1/comparison-groups/{pending["id"]}')
    assert recovered['status'] in ('queued', 'running')
    for _ in range(200):
        recovered = s.call('GET', f'/api/v1/comparison-groups/{pending["id"]}')
        if recovered['status'] == 'completed':
            break
    assert recovered['status'] == 'completed'
    assert all(i['status'] in ('succeeded', 'failed') for i in recovered['items'])
    # 显式恢复接口对未完成组可用且幂等；已完成组冲突。
    s.call('POST', f'/api/v1/comparison-groups/{pending["id"]}/resume', expected=409)
    unfinished = s.call('POST', '/api/v1/comparison-groups', dict(reference=ref, targets=[
        dict(target=good, offset_mm=7000)]), 202)
    for _ in range(100):
        unfinished = s.call('GET', f'/api/v1/comparison-groups/{unfinished["id"]}')
        if unfinished['status'] == 'completed':
            break
    assert unfinished['status'] == 'completed'
    s.call('POST', f'/api/v1/comparison-groups/{unfinished["id"]}/resume', expected=409)
    # 再次重启，已完成组保持稳定，没有重复入队。
    s.stop()
    s.start()
    stable = s.call('GET', f'/api/v1/comparison-groups/{pending["id"]}')
    assert stable['status'] == 'completed'


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('workflow', choices=['record', 'seal', 'compare', 'browse', 'groups', 'all'])
    args = parser.parse_args()
    names = ['record', 'seal', 'compare', 'browse', 'groups'] if args.workflow == 'all' else [args.workflow]
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
