#!/usr/bin/env python3
"""Run bounded HTTP checks in a disposable data directory using the real server."""
import argparse
import concurrent.futures
import csv
import datetime as _dt
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
    def __init__(self, directory, key_path=None):
        self.directory = Path(directory)
        self.binary = self.directory / 'stratad'
        self.verifier = self.directory / 'strata-verify'
        self.keygen = self.directory / 'strata-keygen'
        args = ['go', 'build']
        if os.environ.get('STRATA_SMOKE_RACE') == '1':
            args.append('-race')
        subprocess.run(args + ['-o', str(self.binary), './cmd/stratad'], cwd=ROOT, check=True, timeout=60)
        subprocess.run(['go', 'build', '-o', str(self.verifier), './cmd/strata-verify'], cwd=ROOT, check=True, timeout=60)
        subprocess.run(['go', 'build', '-o', str(self.keygen), './cmd/strata-keygen'], cwd=ROOT, check=True, timeout=60)
        self.key_path = Path(key_path) if key_path else self.directory / 'signing.pem'
        if key_path is None:
            subprocess.run([str(self.keygen), '-out', str(self.key_path), '-force'],
                           cwd=ROOT, check=True, timeout=30, stdout=subprocess.DEVNULL)
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
            [str(self.binary), '-addr', '127.0.0.1:0', '-data', str(self.directory / 'data'),
             '-signing-key', str(self.key_path)],
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
    attempt = subprocess.run([str(s.binary), '-addr', '127.0.0.1:0', '-data', str(s.directory/'data'), '-signing-key', str(s.key_path)], capture_output=True, timeout=10)
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
    attempt = subprocess.run([str(s.binary), '-addr', '127.0.0.1:0', '-data', str(s.directory/'data'), '-signing-key', str(s.key_path)], capture_output=True, timeout=10)
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


def attest(s):
    a, b = sealed(s, '凭据西剖面'), sealed(s, '凭据东剖面', 6000)
    request = dict(left=dict(id=a['id'], version=3), right=dict(id=b['id'], version=3), offset_mm=0)
    comparison = s.call('POST', '/api/v1/comparisons', request, 201)
    cid = comparison['id']
    s.call('POST', f'/api/v1/comparisons/prf_{"0"*32}/credential', {}, expected=404)
    issued = s.call('POST', f'/api/v1/comparisons/{cid}/credential', dict(ttl_hours=168), 201)
    cred_id = issued['credential']['payload']['credential_id']
    assert issued['content_type'] == 'text/csv; charset=utf-8'
    assert issued['digest'] and issued['content_length'] > 0
    # A second issuance while the first is live is rejected; the credential must be explicit.
    s.call('POST', f'/api/v1/comparisons/{cid}/credential', {}, expected=409)
    s.call('POST', f'/api/v1/comparisons/{cid}/credential', dict(ttl_hours=90000), expected=422)
    # The single export source: downloaded bytes must match what was signed.
    csv_bytes = s.call('GET', f'/api/v1/comparisons/{cid}/csv', raw=True).encode()
    assert hashlib.sha256(csv_bytes).hexdigest() == issued['digest']
    assert len(csv_bytes) == issued['content_length']
    # Public key is published; private key never appears in API responses.
    key_info = s.call('GET', '/api/v1/signing-key')
    assert key_info['algorithm'] == 'Ed25519' and 'BEGIN PUBLIC KEY' in key_info['public_key_pem']
    workdir = s.directory / 'verify'
    workdir.mkdir(exist_ok=True)
    public_path = workdir / 'public.pem'
    public_path.write_text(key_info['public_key_pem'])
    cred_path = workdir / 'credential.json'
    cred_path.write_text(json.dumps(issued['credential'], ensure_ascii=False))
    csv_path = workdir / 'comparison.csv'
    csv_path.write_bytes(csv_bytes)
    crl = s.call('GET', '/api/v1/revocation-list')
    assert crl['payload']['revocations'] == [] and crl['signature']
    assert crl['payload']['version'] == 1
    crl_path = workdir / 'crl.json'
    crl_path.write_text(json.dumps(crl))
    assert s.call('GET', f'/api/v1/comparisons/{cid}/credential')['payload']['credential_id'] == cred_id
    assert s.call('GET', f'/api/v1/credentials/{cred_id}') == issued['credential']

    def run_verifier(now=None, crl_override=None, content_override=None, public_override=None):
        args = [str(s.verifier), '-public', str(public_override or public_path),
                '-credential', str(cred_path),
                '-content', str(content_override or csv_path),
                '-crl', str(crl_override or crl_path)]
        if now:
            args += ['-now', now]
        proc = subprocess.run(args, capture_output=True, text=True, timeout=15)
        result = json.loads(proc.stdout)
        return proc.returncode, result

    code, result = run_verifier()
    assert code == 0 and result['verdict'] == 'valid', result
    # One changed byte anywhere in the actual export must fail verification.
    altered = workdir / 'altered.csv'
    tampered = bytearray(csv_bytes)
    tampered[-2] ^= 0x01
    altered.write_bytes(bytes(tampered))
    code, result = run_verifier(content_override=altered)
    assert code == 1 and result['verdict'] == 'invalid', result
    # An unrelated public key cannot validate the signature.
    other_private = workdir / 'other.pem'
    other_public = workdir / 'other-public.pem'
    subprocess.run([str(s.keygen), '-out', str(other_private), '-public-out', str(other_public), '-force'],
                   check=True, stdout=subprocess.DEVNULL, timeout=30)
    code, result = run_verifier(public_override=other_public)
    assert code == 1 and result['verdict'] == 'invalid' and '密钥' in result['reason'], result
    # Expiry yields a definite conclusion.
    code, result = run_verifier(now='2030-01-01T00:00:00Z')
    assert code == 2 and result['verdict'] == 'expired', result
    # Past the CRL window the service refuses to vouch for revocation status.
    next_update = _dt.datetime.fromisoformat(crl['payload']['next_update'].replace('Z', '+00:00'))
    future = (next_update + _dt.timedelta(hours=1)).strftime('%Y-%m-%dT%H:%M:%SZ')
    code, result = run_verifier(now=future)
    assert code == 4 and result['verdict'] == 'crl_stale', result
    # Revocation is immediately visible in a freshly signed list.
    revoked_crl = s.call('POST', f'/api/v1/credentials/{cred_id}/revoke', dict(reason='资料勘误'))['crl']
    assert len(revoked_crl['payload']['revocations']) == 1
    assert revoked_crl['payload']['version'] == 2
    crl_path.write_text(json.dumps(revoked_crl))
    code, result = run_verifier()
    assert code == 3 and result['verdict'] == 'revoked' and result['revoke_reason'] == '资料勘误', result
    # Revoking twice is rejected.
    s.call('POST', f'/api/v1/credentials/{cred_id}/revoke', dict(reason='再次吊销'), expected=409)
    # After revocation a replacement credential can be issued, and it verifies.
    replacement = s.call('POST', f'/api/v1/comparisons/{cid}/credential', dict(ttl_hours=1), 201)
    assert replacement['credential']['payload']['credential_id'] != cred_id
    cred_path.write_text(json.dumps(replacement['credential']))
    crl2 = s.call('GET', '/api/v1/revocation-list')
    crl_path.write_text(json.dumps(crl2))
    code, result = run_verifier()
    assert code == 0 and result['verdict'] == 'valid', result
    # Restart must re-verify every persisted signature and keep serving.
    s.stop()
    s.start()
    # A time-only CRL refresh at startup must not advance the version.
    assert s.call('GET', '/api/v1/revocation-list')['payload']['version'] == 2
    assert s.call('GET', f'/api/v1/credentials/{cred_id}')['payload']['credential_id'] == cred_id
    code, _ = run_verifier()
    assert code == 0
    # The revoked credential keeps its definite conclusion across restart too.
    cred_path.write_text(json.dumps(issued['credential']))
    code, _ = run_verifier()
    assert code == 3
    # Startup without the issuing key is refused with a clear reason.
    s.stop()
    attempt = subprocess.run([str(s.binary), '-addr', '127.0.0.1:0', '-data', str(s.directory/'data')],
                             capture_output=True, timeout=10)
    assert attempt.returncode != 0 and '未配置签发私钥' in attempt.stderr.decode()
    # Startup with a different key is refused because data does not match the key.
    attempt = subprocess.run([str(s.binary), '-addr', '127.0.0.1:0', '-data', str(s.directory/'data'),
                              '-signing-key', str(other_private)], capture_output=True, timeout=10)
    assert attempt.returncode != 0 and ('其他签发密钥' in attempt.stderr.decode() or '签名核验失败' in attempt.stderr.decode())
    # Tampering with a persisted credential signature is rejected at startup.
    # The outer snapshot digest is recomputed so only the inner Ed25519
    # signature is broken, exercising key-based verification specifically.
    snapshot = s.directory / 'data' / 'strata.json'
    original = snapshot.read_bytes()
    envelope = json.loads(original)
    first = next(iter(envelope['data']['credentials']))
    envelope['data']['credentials'][first]['signature'] = '00' * 64
    data_raw = json.dumps(envelope['data'], ensure_ascii=False, separators=(',', ':')).encode()
    envelope['digest'] = hashlib.sha256(data_raw).hexdigest()
    snapshot.write_bytes(json.dumps(envelope, ensure_ascii=False, separators=(',', ':')).encode())
    attempt = subprocess.run([str(s.binary), '-addr', '127.0.0.1:0', '-data', str(s.directory/'data'),
                              '-signing-key', str(s.key_path)], capture_output=True, timeout=10)
    assert attempt.returncode != 0 and '签名核验失败' in attempt.stderr.decode(), attempt.stderr.decode()
    snapshot.write_bytes(original)
    s.start()
    # Switch back to the revoked credential: its conclusion must survive restart.
    cred_path.write_text(json.dumps(issued['credential']))
    code, _ = run_verifier()
    assert code == 3


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('workflow', choices=['record', 'seal', 'compare', 'browse', 'attest', 'all'])
    args = parser.parse_args()
    names = ['record', 'seal', 'compare', 'browse', 'attest'] if args.workflow == 'all' else [args.workflow]
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
