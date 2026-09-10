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

    def call_csv(self, path, text, expected=200, method='POST', ctype='text/csv; charset=utf-8'):
        request = urllib.request.Request(self.url + path, data=text.encode('utf-8'), method=method,
                                         headers={'Content-Type': ctype})
        try:
            response = urllib.request.urlopen(request, timeout=10)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            payload = response.read()
            if response.status != expected:
                raise RuntimeError(f'{method} {path}: expected {expected}, got {response.status}: {payload.decode()}')
            location = response.headers.get('Location')
            status = response.status
        parsed = None if not payload else json.loads(payload)
        return parsed, location, status

    def call_status(self, method, path, body=None):
        data = None if body is None else json.dumps(body, ensure_ascii=False).encode()
        request = urllib.request.Request(self.url + path, data=data, method=method,
                                         headers={'Content-Type': 'application/json'})
        try:
            response = urllib.request.urlopen(request, timeout=10)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            payload = response.read()
            return response.status, (json.loads(payload) if payload else None)


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


def imports(s):
    header = 'name,site,depth_mm,note,top_mm,bottom_mm,rock,description,marker'
    good = '\r\n'.join([
        header,
        '东沟剖面,东沟,10000,砂泥岩序列,0,4000,sandstone,细砂岩,',
        '东沟剖面,东沟,10000,砂泥岩序列,4000,10000,mudstone,泥岩,凝灰标志',
        '西梁剖面,西梁,6000,灰岩剖面,0,6000,limestone,灰岩,',
    ]) + '\r\n'

    # media type checks
    request = urllib.request.Request(s.url + '/api/v1/profile-imports', data=b'x', method='POST',
                                     headers={'Content-Type': 'application/json'})
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            raise AssertionError('expected 415, got ' + str(response.status))
    except urllib.error.HTTPError as error:
        assert error.code == 415, error.code
        error.read()

    # structural CSV problems never reach a preview (422)
    for bad in [header + '\n', 'nope,rock\n', 'name,site,depth_mm,note,top_mm,bottom_mm,rock,description,marker,extra\n']:
        payload, _, status = s.call_csv('/api/v1/profile-imports', bad, expected=422)
        assert payload['error']['code'] == 'invalid'

    job, location, status = s.call_csv('/api/v1/profile-imports', good, expected=201)
    assert status == 201 and location == '/api/v1/profile-imports/' + job['id']
    assert job['status'] == 'preview'
    assert [g['name'] for g in job['groups']] == ['东沟剖面', '西梁剖面']
    east = job['groups'][0]
    assert east['depth_mm'] == 10000 and len(east['layers']) == 2
    assert east['layers'][0]['line'] == 2 and east['layers'][1]['line'] == 3
    assert 'source_csv' not in job
    assert s.call('GET', f"/api/v1/profile-imports/{job['id']}")['id'] == job['id']

    # identical file reuses the same job, never runs it twice
    again, _, status = s.call_csv('/api/v1/profile-imports', good, expected=200)
    assert status == 200 and again['id'] == job['id']

    # bad batch: line-numbered errors with raw fields, nothing is created
    bad = '\n'.join([
        header,
        '北坡,北坡,10000,n,0,5000,sandstone,好,',
        '北坡,北坡,10000,n,4000,10000,mudstone,重叠,',
        '北坡,北坡,9000,n,0,1000,sandstone,深度不一致,',
        '南坡,南坡,8000,n,0,9000,granite,错岩性,',
        '东坡,东坡,abc,n,0,1000,sandstone,深度非整数,',
        '西坡,西坡,10000,n,1000,500,sandstone,非正厚度,',
        ' ,北坡,10000,n,0,1000,sandstone,无名称,',
        '北坡,北坡,10000,n,0,1000,sandstone,缺列',
    ])
    badjob, _, _ = s.call_csv('/api/v1/profile-imports', bad, expected=201)
    assert badjob['status'] == 'preview' and badjob['groups']
    lines = {e['line']: e for e in badjob['errors']}
    assert set(lines) == {2, 4, 5, 6, 7, 8, 9}, lines

    # Every RowError must bind the COMPLETE original nine-column record of
    # its source line, including metadata and layers cross-row failures.
    raw_columns = ['name', 'site', 'depth_mm', 'note', 'top_mm', 'bottom_mm',
                   'rock', 'description', 'marker']
    bad_lines = bad.splitlines()
    for err in badjob['errors']:
        cells = next(csv.reader([bad_lines[err['line'] - 1]]))
        cells = (cells + [''] * len(raw_columns))[:len(raw_columns)]
        expected = dict(zip(raw_columns, cells))
        got = {col: err['raw'].get(col, '') for col in raw_columns}
        assert got == expected, (err['line'], err['field'], got, expected)
        assert not err['raw'].get('extra'), err  # well-formed rows carry no extra cells
    # the ragged row keeps its 8 cells in column order and has no overflow cells
    assert 'extra' not in lines[9]['raw']

    # overlap is reported on the earlier layer's line and keeps that raw row
    assert lines[2]['field'] == 'layers' and lines[2]['detail'] == '分层不能重叠'
    assert lines[2]['raw']['name'] == '北坡' and lines[2]['raw']['site'] == '北坡'
    assert lines[2]['raw']['depth_mm'] == '10000' and lines[2]['raw']['bottom_mm'] == '5000'
    assert lines[4]['field'] == 'depth_mm' and lines[4]['raw']['depth_mm'] == '9000'
    assert lines[4]['raw']['name'] == '北坡' and lines[4]['raw']['rock'] == 'sandstone'
    assert lines[5]['field'] == 'rock' and lines[5]['raw']['rock'] == 'granite'
    assert lines[5]['raw']['name'] == '南坡' and lines[5]['raw']['bottom_mm'] == '9000'
    assert lines[6]['field'] == 'depth_mm'
    assert lines[6]['raw']['name'] == '东坡' and lines[6]['raw']['top_mm'] == '0'
    assert lines[7]['field'] == 'layers' and lines[7]['raw']['bottom_mm'] == '500'
    assert lines[7]['raw']['name'] == '西坡' and lines[7]['raw']['depth_mm'] == '10000'
    assert lines[8]['field'] == 'name' and lines[8]['raw']['name'] == ' '
    assert lines[9]['field'] == 'row' and lines[9]['raw']['rock'] == 'sandstone'
    assert lines[9]['raw']['marker'] == '' and lines[9]['raw']['description'] == '缺列'

    # metadata-only failure (depth out of range) still carries all layer columns
    meta = '\n'.join([
        header,
        '深剖面,深地点,0,说明留空,100,200,sandstone,首层,标志A',
        '深剖面,深地点,0,说明留空,200,400,mudstone,二层,标志B',
    ])
    metajob, _, _ = s.call_csv('/api/v1/profile-imports', meta, expected=201)
    assert len(metajob['errors']) == 1 and metajob['errors'][0]['line'] == 2
    mraw = metajob['errors'][0]['raw']
    assert mraw == {'name': '深剖面', 'site': '深地点', 'depth_mm': '0', 'note': '说明留空',
                    'top_mm': '100', 'bottom_mm': '200', 'rock': 'sandstone',
                    'description': '首层', 'marker': '标志A'}, mraw

    # duplicate marker error (cross-row layers rule) carries the full row
    markers = '\n'.join([
        header,
        '标剖面,标地点,10000,,0,4000,sandstone,首层,凝灰',
        '标剖面,标地点,10000,,4000,10000,mudstone,二层,凝灰',
    ])
    markerjob, _, _ = s.call_csv('/api/v1/profile-imports', markers, expected=201)
    merr = next(e for e in markerjob['errors'] if '标志层' in e['detail'])
    assert merr['line'] == 3 and merr['raw']['name'] == '标剖面'
    assert merr['raw']['depth_mm'] == '10000' and merr['raw']['marker'] == '凝灰'
    assert merr['raw']['top_mm'] == '4000' and merr['raw']['bottom_mm'] == '10000'

    # over-500-layers group-level error binds the first row's full record
    cap_rows = [header]
    for i in range(501):
        cap_rows.append(f'厚剖面,厚地点,1000000,,{i*1000},{(i+1)*1000},sandstone,第{i}层,')
    capjob, _, _ = s.call_csv('/api/v1/profile-imports', '\n'.join(cap_rows), expected=201)
    caperr = next(e for e in capjob['errors'] if '500' in e['detail'])
    assert caperr['line'] == 2 and caperr['field'] == 'layers'
    assert caperr['raw'] == {'name': '厚剖面', 'site': '厚地点', 'depth_mm': '1000000',
                             'note': '', 'top_mm': '0', 'bottom_mm': '1000',
                             'rock': 'sandstone', 'description': '第0层', 'marker': ''}, caperr['raw']

    status, payload = s.call_status('POST', f"/api/v1/profile-imports/{badjob['id']}/confirm")
    assert status == 422 and payload['import']['status'] == 'failed'
    assert payload['import']['id'] == badjob['id']
    # failed import cannot run again
    status, payload = s.call_status('POST', f"/api/v1/profile-imports/{badjob['id']}/confirm", {'reason': '重试'})
    assert status == 409, status
    # nothing was created for the bad batch
    assert s.call('GET', '/api/v1/profiles?q=' + urllib.parse.quote('北坡'))['total'] == 0

    # preview survives restart
    s.stop()
    s.start()
    reloaded = s.call('GET', f"/api/v1/profile-imports/{badjob['id']}")
    assert reloaded['status'] == 'failed' and len(reloaded['errors']) == len(badjob['errors'])

    # confirming the clean batch creates drafts atomically
    status, payload = s.call_status('POST', f"/api/v1/profile-imports/{job['id']}/confirm", {'reason': '现场 CSV 验收'})
    assert status == 200, payload
    completed = payload
    assert completed['status'] == 'completed' and len(completed['profile_ids']) == 2
    east_id, west_id = completed['profile_ids']
    east_now = s.call('GET', f'/api/v1/profiles/{east_id}')
    assert east_now['version'] == 1 and east_now['state'] == 'draft'
    assert [l['rock'] for l in east_now['layers']] == ['sandstone', 'mudstone']
    assert east_now['layers'][1]['marker'] == '凝灰标志'
    history = s.call('GET', f'/api/v1/profiles/{east_id}/history')
    assert history['items'][0]['reason'] == '现场 CSV 验收'
    west_now = s.call('GET', f'/api/v1/profiles/{west_id}')
    assert west_now['depth_mm'] == 6000 and west_now['layers'][0]['rock'] == 'limestone'

    # completed import cannot run again, and re-upload cannot re-execute
    assert s.call_status('POST', f"/api/v1/profile-imports/{job['id']}/confirm", {'reason': '再试'})[0] == 409
    again, _, status = s.call_csv('/api/v1/profile-imports', good, expected=200)
    assert again['status'] == 'completed' and again['profile_ids'] == completed['profile_ids']
    assert s.call_status('POST', f"/api/v1/profile-imports/{job['id']}/confirm")[0] == 409

    # missing import is 404, including at confirmation
    s.call('GET', '/api/v1/profile-imports/imp_' + '0' * 32, expected=404)
    assert s.call_status('POST', '/api/v1/profile-imports/imp_' + '0' * 32 + '/confirm', {'reason': 'x'})[0] == 404

    # restart with completed imports: profiles and import status both recover
    s.stop()
    s.start()
    assert s.call('GET', f'/api/v1/profiles/{east_id}') == east_now
    final = s.call('GET', f"/api/v1/profile-imports/{job['id']}")
    assert final['status'] == 'completed' and final['profile_ids'] == completed['profile_ids']


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('workflow', choices=['record', 'seal', 'compare', 'browse', 'imports', 'all'])
    args = parser.parse_args()
    names = ['record', 'seal', 'compare', 'browse', 'imports'] if args.workflow == 'all' else [args.workflow]
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
