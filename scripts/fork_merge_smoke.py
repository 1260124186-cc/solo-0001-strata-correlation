#!/usr/bin/env python3
"""End-to-end checks for parallel revision lines (fork/relation/merge).

Runs against a disposable server just like scripts/smoke.py. Exits non-zero on
the first failed assertion.
"""
import json
import os
import sys
import tempfile

sys.path.insert(0, os.path.join(os.path.dirname(__file__), '..', 'scripts'))
from smoke import Server  # noqa: E402


def must(cond, detail):
    if not cond:
        raise AssertionError(detail)


def main():
    with tempfile.TemporaryDirectory() as directory:
        s = Server(directory)
        try:
            # 主线：建剖面 → 完整两层 → 锁定 → 重新打开。
            p = s.call('POST', '/api/v1/profiles',
                       dict(name='分叉剖面', site='赤石岭', depth_mm=10000, note=''), 201)
            pid = p['id']
            base_layers = [
                dict(top_mm=0, bottom_mm=4000, rock='sandstone', description='主线砂岩', marker=''),
                dict(top_mm=4000, bottom_mm=10000, rock='mudstone', description='主线泥岩', marker='凝灰标志'),
            ]
            s.call('PUT', f'/api/v1/profiles/{pid}/layers',
                   dict(expected_version=1, reason='初版分层', layers=base_layers))
            v2 = s.call('GET', f'/api/v1/profiles/{pid}')
            must(v2['version'] == 2 and v2['branch'] == 'main', '主线头应为 v2/main')
            v3 = s.call('POST', f'/api/v1/profiles/{pid}/seal',
                        dict(expected_version=2, reason='锁定基线'))
            must(v3['version'] == 3 and v3['state'] == 'sealed', 'v3 应为锁定')
            v4 = s.call('POST', f'/api/v1/profiles/{pid}/reopen',
                        dict(expected_version=3, reason='主线继续修订'))
            must(v4['version'] == 4 and v4['state'] == 'draft', 'v4 主线重开')

            # 从历史版本 3（锁定基线）固定分叉，而不是从头分叉。
            forked = s.call('POST', f'/api/v1/profiles/{pid}/fork',
                            dict(source_version=3, name='野外补测线', reason='现场第二意见'), 201)
            must(forked['version'] == 5 and forked['state'] == 'draft',
                 f"分叉应产生 v5 草拟，实际 {forked.get('version')}")
            branch_id = None
            listing = s.call('GET', f'/api/v1/profiles/{pid}/branches')
            must(listing['total'] == 2, '应有主线与一条分叉')
            for b in listing['items']:
                if not b['is_main']:
                    branch_id = b['id']
                    must(b['fork_point'] == 3, '分叉点必须固定为来源版本 3')
                    must(b['name'] == '野外补测线', '分叉名称应保存')
            must(branch_id and branch_id.startswith('br_'), '分叉编号缺失')

            # 分叉事件必须写清分叉点。
            rev5 = s.call('GET', f'/api/v1/profiles/{pid}/revisions/5')
            must(rev5['event']['action'] == 'fork' and rev5['event']['fork_point'] == 3
                 and rev5['event']['branch'] == branch_id, '分叉事件缺少分叉点/归属')
            must(rev5['profile']['branch'] == branch_id, '分叉剖面归属错误')
            # 分叉内容固定为来源版本 3（锁定状态被重开为 draft，内容不变）。
            must(rev5['profile']['layers'] == base_layers, '分叉内容必须等于来源版本')

            # 关系查询：相对主线 ahead/behind/fork_point/merge_base。
            relation = s.call('GET', f'/api/v1/profiles/{pid}/branches/{branch_id}/relation')
            must(relation['fork_point'] == 3 and relation['merge_base'] == 3, '基点应为 3')
            must(relation['ahead'] == 1 and relation['behind'] == 1,
                f"分叉后 ahead=1/behind=1，实际 {relation['ahead']}/{relation['behind']}")
            must(relation['merged'] is False, '尚未合并')

            # 两条线各自继续编录，互不影响。
            # 主线 v6：改元数据名称。
            main_edit = s.call('PUT', f'/api/v1/profiles/{pid}',
                               dict(expected_version=4, reason='主线改名',
                                    metadata=dict(name='分叉剖面-主线修订', site='赤石岭',
                                                  depth_mm=10000, note='')))
            must(main_edit['version'] == 6 and main_edit['branch'] == 'main', '主线应到 v6')

            # 分叉线 v7：替换分层（在自己的线上继续）。
            branch_layers = [
                dict(top_mm=0, bottom_mm=3000, rock='sandstone', description='分叉砂岩', marker=''),
                dict(top_mm=3000, bottom_mm=10000, rock='limestone', description='分叉灰岩', marker='凝灰标志'),
            ]
            br_edit = s.call('PUT', f'/api/v1/profiles/{pid}/layers',
                             dict(expected_version=5, branch=branch_id, reason='分叉改分层',
                                  layers=branch_layers))
            must(br_edit['version'] == 7 and br_edit['branch'] == branch_id, '分叉线应到 v7')

            # 分叉线独立锁定；主线保持 draft，互不影响。
            br_sealed = s.call('POST', f'/api/v1/profiles/{pid}/seal',
                               dict(expected_version=7, branch=branch_id, reason='分叉线锁定'))
            must(br_sealed['version'] == 8 and br_sealed['state'] == 'sealed', '分叉线应锁定为 v8')
            main_head = s.call('GET', f'/api/v1/profiles/{pid}')
            must(main_head['version'] == 6 and main_head['state'] == 'draft', '主线不应被分叉锁定影响')

            # 历史不再按位置推断：v6 是主线编辑、v7 是分叉编辑。
            full = s.call('GET', f'/api/v1/profiles/{pid}/history?limit=100')
            actions = {e['version']: (e['action'], e.get('branch')) for e in full['items']}
            must(actions[6][0] == 'metadata' and actions[6][1] == 'main', 'v6 必须是主线 metadata')
            must(actions[7][0] == 'layers' and actions[7][1] == branch_id, 'v7 必须是分叉 layers')
            must(actions[8][0] == 'seal' and actions[8][1] == branch_id, 'v8 必须是分叉 seal')
            main_only = s.call('GET', f'/api/v1/profiles/{pid}/history?branch=main&limit=100')
            must(all(e.get('branch', 'main') == 'main' for e in main_only['items']),
                '主线历史不应包含分叉版本')

            # 跨线差异按真实版本号工作（v3→v7 无版本顺序要求）。
            cross = s.call('GET', f'/api/v1/profiles/{pid}/diff?from=3&to=7')
            must(len(cross['layers']) >= 2, '跨线差异应列出分层变化')

            # 深度查询可指定线与具体版本。
            at_branch = s.call('GET', f'/api/v1/profiles/{pid}/at?depth_mm=3500&branch={branch_id}')
            must(at_branch['version'] == 8, '分叉线头深度查询版本应为 8')
            at_v6 = s.call('GET', f'/api/v1/profiles/{pid}/at?depth_mm=3500&version=6')
            must(at_v6['version'] == 6, '显式版本查询必须命中 v6 而非线头')

            # 合并预览：基点 3；主线只改了 name，分叉只改了 layers → 应自动合并。
            preview = s.call('POST', f'/api/v1/profiles/{pid}/merge-preview',
                             dict(source=branch_id))
            must(preview['mergeable'] is True, '无冲突时必须可合并')
            must(preview['base_version'] == 3 and preview['ahead'] >= 3 and preview['behind'] == 2,
                '预览基点/ahead/behind 不正确')
            must(preview['report']['field_conflicts'] == [] and
                preview['report']['layer_conflicts'] == [], '不应有冲突')
            must(preview['report']['merged']['name'] == '分叉剖面-主线修订', '应采纳主线改名')
            must(len(preview['report']['merged']['layers']) == 2 and
                preview['report']['merged']['layers'][1]['rock'] == 'limestone', '应采纳分叉分层')

            # 错误预期版本必须冲突。
            s.call('POST', f'/api/v1/profiles/{pid}/merge',
                   dict(source=branch_id, expected_version=4, reason='合并分叉'), 409)
            # 执行合并：产生新版本（v9）而非覆盖。
            merged = s.call('POST', f'/api/v1/profiles/{pid}/merge',
                            dict(source=branch_id, expected_version=6,
                                 expected_source_version=8, reason='合并分叉回主线'), 201)
            must(merged['version'] == 9 and merged['branch'] == 'main'
                 and merged['state'] == 'draft', '合并必须在主线产生新草拟版本 v9')
            must(merged['name'] == '分叉剖面-主线修订'
                 and merged['layers'][1]['rock'] == 'limestone', '合并内容应融合双方修改')

            rev9 = s.call('GET', f'/api/v1/profiles/{pid}/revisions/9')
            ref = rev9['event']['merge']
            must(ref and ref['source'] == branch_id and ref['source_version'] == 8
                 and ref['base_version'] == 3, '合并事件必须记录来源线、来源版本与基点')
            must(rev9['event']['parent'] == 6, '合并版本的第一父应是主线头 v6')

            # 被合并的来源版本仍可读，分叉线头不被删除。
            still = s.call('GET', f'/api/v1/profiles/{pid}/revisions/8')
            must(still['profile']['state'] == 'sealed' and still['profile']['branch'] == branch_id,
                '被合并的来源版本必须仍然可读')
            relation2 = s.call('GET', f'/api/v1/profiles/{pid}/branches/{branch_id}/relation')
            must(relation2['merged'] is True and relation2['merge_base'] == 8
                and relation2['ahead'] == 0 and relation2['behind'] >= 1,
                '合并后关系应反映已并入主线')

            # 历史版本总数：9，且重启后全部可恢复。
            s.stop()
            s.start()
            head = s.call('GET', f'/api/v1/profiles/{pid}')
            must(head['version'] == 9 and head['layers'][1]['rock'] == 'limestone',
                '重启后合并结果必须可恢复')
            listing2 = s.call('GET', f'/api/v1/profiles/{pid}/branches')
            must(listing2['total'] == 2, '重启后分叉登记必须保留')

            # 冲突场景：新开一条线，双方改同一字段为不同值。
            f2 = s.call('POST', f'/api/v1/profiles/{pid}/fork',
                        dict(source_version=9, name='冲突线', reason='测试冲突'), 201)
            b2 = None
            for b in s.call('GET', f'/api/v1/profiles/{pid}/branches')['items']:
                if b.get('name') == '冲突线':
                    b2 = b['id']
            s.call('PUT', f'/api/v1/profiles/{pid}',
                   dict(expected_version=9, reason='主线改地点',
                        metadata=dict(name='分叉剖面-主线修订', site='主线地点',
                                      depth_mm=10000, note='')))
            main_after_rename = s.call('GET', f'/api/v1/profiles/{pid}')
            s.call('PUT', f'/api/v1/profiles/{pid}',
                   dict(expected_version=f2['version'], branch=b2, reason='分叉改地点',
                        metadata=dict(name='分叉剖面-主线修订', site='分叉地点',
                                      depth_mm=10000, note='')))
            conflict_preview = s.call('POST', f'/api/v1/profiles/{pid}/merge-preview',
                                      dict(source=b2), 409)
            fields = {c['field'] for c in conflict_preview['report']['field_conflicts']}
            must('site' in fields and conflict_preview['mergeable'] is False,
                '同字段不同值必须报告冲突并禁止合并')
            # 冲突合并必须被拒绝，且不产生新版本（主线头仍是改名后的版本）。
            s.call('POST', f'/api/v1/profiles/{pid}/merge',
                   dict(source=b2, expected_version=main_after_rename['version'],
                        reason='强行合并'), 409)
            unchanged = s.call('GET', f'/api/v1/profiles/{pid}')
            must(unchanged['version'] == main_after_rename['version']
                 and unchanged['site'] == '主线地点',
                '冲突合并不得写入或覆盖任何版本')

            # 从任意历史版本（而非仅线头）分叉应当被允许并固定该点。
            f3 = s.call('POST', f'/api/v1/profiles/{pid}/fork',
                        dict(source_version=6, name='旧版分叉', reason='回到 v6'), 201)
            rev_f3 = s.call('GET', f'/api/v1/profiles/{pid}/revisions/%d' % f3['version'])
            must(rev_f3['event']['fork_point'] == 6, '应能从任意历史版本分叉')

        finally:
            s.close()
    print('fork/merge workflow passed')


if __name__ == '__main__':
    main()
