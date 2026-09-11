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

    def diag(self, expected=0):
        """Run the read-only diagnostic mode and parse its JSON report."""
        result = subprocess.run(
            [str(self.binary), '-diag', '-data', str(self.directory / 'data')],
            capture_output=True, text=True, timeout=10,
        )
        assert result.returncode == expected, (result.returncode, result.stderr)
        return json.loads(result.stdout), result

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


def diagnose(s):
    # 空目录：判定可启动且不创建任何快照文件。
    s.stop()
    report, _ = s.diag()
    data_dir = s.directory / 'data'
    assert report['startable'] is True and report['snapshot']['present'] is False
    assert all(area['status'] == 'pass' for area in report['areas'])
    assert not (data_dir / 'strata.json').exists()
    s.start()

    a, b = sealed(s, '西侧诊断剖面'), sealed(s, '东侧诊断剖面', 6000)
    request = dict(left=dict(id=a['id'], version=3), right=dict(id=b['id'], version=3), offset_mm=0)
    comparison = s.call('POST', '/api/v1/comparisons', request, 201)

    # 健康数据：诊断通过，随后正常启动不得因同一问题失败。
    s.stop()
    report, _ = s.diag()
    assert report['startable'] is True
    assert report['snapshot']['digest_ok'] is True
    assert report['counts'] == {'profiles': 2, 'revisions': 6, 'comparisons': 1}
    assert all(area['status'] == 'pass' for area in report['areas'])
    assert report['recoverability']['auto_recoverable'] is True
    leaked = json.dumps(report, ensure_ascii=False)
    for secret in ('西侧诊断剖面', '东侧诊断剖面', '凝灰标志'):
        assert secret not in leaked

    # 遗留临时文件只产生 warning，不影响可启动结论，且不被自动清理。
    leftover = data_dir / '.strata-smoke-leftover'
    leftover.write_text('partial')
    report, _ = s.diag()
    assert report['startable'] is True and report['leftover_temp_files'] == 1
    recovery = next(a for a in report['areas'] if a['name'] == 'recoverability')
    assert recovery['status'] == 'warn'
    assert recovery['findings'] == [{
        'code': 'leftover_temp_files', 'area': 'recoverability',
        'severity': 'warning', 'detail': recovery['findings'][0]['detail'],
    }]
    assert leftover.exists()

    snapshot = data_dir / 'strata.json'
    original = snapshot.read_bytes()

    def area_of(report, name):
        return next(a for a in report['areas'] if a['name'] == name)

    def codes(report, name):
        return [(f['code'], f['severity']) for f in area_of(report, name)['findings']]

    def assert_fatal(report, normal_stderr):
        """任何致命失败：受影响区域为 fail/blocked，可恢复性必须保持 fail，
        遗留临时文件只能作为附加 warning 存在。"""
        assert report['startable'] is False
        assert report['recoverability']['status'] == 'fail'
        assert report['recoverability']['auto_recoverable'] is False
        recovery_area = area_of(report, 'recoverability')
        assert recovery_area['status'] == 'fail'
        assert codes(report, 'recoverability') == [
            ('snapshot_unstartable', 'error'), ('leftover_temp_files', 'warning')]
        assert leftover.exists()
        attempt = subprocess.run([str(s.binary), '-addr', '127.0.0.1:0', '-data', str(data_dir)],
                                 capture_output=True, timeout=10)
        assert attempt.returncode != 0 and normal_stderr in attempt.stderr

    # 校验值损坏叠加遗留临时文件：诊断判为不可启动，后续区域 blocked。
    damaged = json.loads(original)
    damaged['digest'] = '0' * 64
    snapshot.write_text(json.dumps(damaged, ensure_ascii=False))
    report, _ = s.diag(expected=1)
    assert codes(report, 'snapshot_checksum') == [('snapshot_checksum_mismatch', 'error')]
    assert area_of(report, 'version_chain')['status'] == 'blocked'
    assert area_of(report, 'comparison_references')['status'] == 'blocked'
    assert_fatal(report, b'checksum mismatch')

    # 形状不受支持（schema 非法）但摘要有效：版本链给出致命结论，
    # 对比引用实际未执行，必须是 blocked 而不是 pass；可恢复性仍为 fail。
    envelope = json.loads(original)
    envelope['data']['schema'] = 999
    raw = json.dumps(envelope['data'], ensure_ascii=False, separators=(',', ':'))
    digest = hashlib.sha256(raw.encode()).hexdigest()
    snapshot.write_text('{"digest":' + json.dumps(digest) + ',"data":' + raw + '}')
    report, _ = s.diag(expected=1)
    assert report['startable'] is False and report['snapshot']['digest_ok'] is True
    chain = area_of(report, 'version_chain')
    assert chain['status'] == 'fail'
    assert codes(report, 'version_chain') == [('snapshot_shape_unsupported', 'error')]
    assert area_of(report, 'comparison_references')['status'] == 'blocked'
    assert_fatal(report, b'unsupported snapshot shape')

    # 校验值有效但版本链语义损坏：定位到具体剖面版本，结论仍与正常启动一致。
    envelope = json.loads(original)
    profile_id = a['id']
    envelope['data']['histories'][profile_id][1]['event']['action'] = 'bogus'
    raw = json.dumps(envelope['data'], ensure_ascii=False, separators=(',', ':'))
    digest = hashlib.sha256(raw.encode()).hexdigest()
    snapshot.write_text('{"digest":' + json.dumps(digest) + ',"data":' + raw + '}')
    report, _ = s.diag(expected=1)
    assert report['startable'] is False and report['snapshot']['digest_ok'] is True
    chain = area_of(report, 'version_chain')
    assert chain['status'] == 'fail' and chain['findings'][0]['code'] == 'step_unknown_action'
    assert chain['findings'][0]['severity'] == 'error'
    assert chain['findings'][0]['location'] == f'{profile_id}#v2'
    assert_fatal(report, b'unknown revision action')

    # 恢复并清理临时文件后诊断通过，正常启动也必须成功。
    snapshot.write_bytes(original)
    leftover.unlink()
    report, _ = s.diag()
    assert report['startable'] is True
    assert area_of(report, 'recoverability')['status'] == 'pass'
    assert codes(report, 'recoverability') == []
    s.start()
    assert s.call('GET', f'/api/v1/comparisons/{comparison["id"]}') == comparison

    # 运行中的服务持锁，诊断不能并发打开同一数据目录。
    attempt = subprocess.run([str(s.binary), '-diag', '-data', str(data_dir)],
                             capture_output=True, text=True, timeout=10)
    assert attempt.returncode != 0 and 'already in use' in attempt.stderr
    assert attempt.stdout == ''


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('workflow', choices=['record', 'seal', 'compare', 'browse', 'diagnose', 'all'])
    args = parser.parse_args()
    names = ['record', 'seal', 'compare', 'browse', 'diagnose'] if args.workflow == 'all' else [args.workflow]
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
