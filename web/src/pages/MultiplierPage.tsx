import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { BellRing, CheckCircle2, CircleOff, Clock3, Percent, RefreshCw, Save, Search, SquareCheckBig } from 'lucide-react'
import { Link, useSearchParams } from 'react-router-dom'
import { api } from '../api/client'
import { EmptyState, ErrorView, InlineMessage, LoadingView, PageHeader } from '../components/Common'
import { formatDateTime, formatRelativeTime } from '../lib/format'
import { targetKindLabels, type GroupMultiplier, type MultiplierStatus, type TargetMultiplierState } from '../types'

const statusLabels: Record<MultiplierStatus, string> = {
  stable: '稳定',
  changed: '已变化',
  missing: '本次未找到',
  unknown: '待检测'
}

const statusIcons = {
  stable: CheckCircle2,
  changed: BellRing,
  missing: CircleOff,
  unknown: Clock3
}

function groupStatusLabel(group: GroupMultiplier): string {
  if (!group.monitored) return '未监控'
  if (group.lastError) return '检测失败'
  return statusLabels[group.status]
}

function multiplierText(value: string): string {
  // 倍率直接展示服务端十进制字符串，不能转成 JavaScript 浮点数。
  return value ? `${value}×` : '等待建立基准'
}

function selectionChanged(selectedKeys: Set<string>, groups: GroupMultiplier[]): boolean {
  const monitoredKeys = groups.filter((group) => group.monitored).map((group) => group.key)
  return selectedKeys.size !== monitoredKeys.length || monitoredKeys.some((key) => !selectedKeys.has(key))
}

export default function MultiplierPage() {
  const queryClient = useQueryClient()
  const [params, setParams] = useSearchParams()
  const requestedTargetId = params.get('target') ?? ''
  const [targetId, setTargetId] = useState(requestedTargetId)
  const targetIdRef = useRef(targetId)
  const approvedTargetIdRef = useRef<string | null>(null)
  const [detectedState, setDetectedState] = useState<TargetMultiplierState | null>(null)
  const [selectedKeys, setSelectedKeys] = useState<Set<string>>(new Set())
  const [dirty, setDirty] = useState(false)
  const [successMessage, setSuccessMessage] = useState('')

  const targetsQuery = useQuery({ queryKey: ['targets'], queryFn: api.targets })
  const eligibleTargets = useMemo(
    () => (targetsQuery.data ?? []).filter((target) => target.kind === 'new_api' || target.kind === 'sub2api'),
    [targetsQuery.data]
  )
  const selectedTarget = eligibleTargets.find((target) => target.id === targetId)
  const stateQuery = useQuery({
    queryKey: ['group-multipliers', targetId],
    queryFn: () => api.multiplierState(targetId),
    enabled: Boolean(targetId && selectedTarget)
  })

  useEffect(() => {
    targetIdRef.current = targetId
  }, [targetId])

  useEffect(() => {
    const stored = stateQuery.data
    if (!stored || dirty) return
    if (detectedState?.targetId === targetId) {
      const storedByKey = new Map(stored.groups.map((group) => [group.key, group]))
      const mergedGroups = detectedState.groups.map((group) => {
        const latest = storedByKey.get(group.key)
        if (latest) {
          storedByKey.delete(group.key)
          return latest
        }
        return group.monitored ? { ...group, monitored: false, status: 'unknown' as const, lastError: undefined } : group
      })
      mergedGroups.push(...storedByKey.values())
      const merged = { ...detectedState, ...stored, groups: mergedGroups }
      setDetectedState(merged)
      setSelectedKeys(new Set(merged.groups.filter((group) => group.monitored).map((group) => group.key)))
      return
    }
    setSelectedKeys(new Set(stored.groups.filter((group) => group.monitored).map((group) => group.key)))
    setDirty(false)
  }, [dirty, stateQuery.dataUpdatedAt, targetId])

  const applyResult = (result: TargetMultiplierState, message: string) => {
    queryClient.setQueryData(['group-multipliers', result.targetId], result)
    void queryClient.invalidateQueries({ queryKey: ['alerts'] })
    void queryClient.invalidateQueries({ queryKey: ['dashboard'] })
    // 请求结束前若路由已经切换，只更新原渠道缓存，不能覆盖新渠道表单。
    if (result.targetId !== targetIdRef.current) return
    setDetectedState(result)
    setSelectedKeys(new Set(result.groups.filter((group) => group.monitored).map((group) => group.key)))
    setDirty(false)
    setSuccessMessage(message)
  }

  const detectMutation = useMutation({
    mutationFn: api.detectMultiplierGroups,
    onSuccess: (result) => applyResult(result, `已检测到 ${result.groups.length} 个固定倍率分组。`)
  })
  const checkMutation = useMutation({
    mutationFn: api.checkMultiplierGroups,
    onSuccess: (result) => applyResult(result, '当前渠道倍率已经刷新。')
  })
  const saveMutation = useMutation({
    mutationFn: ({ id, keys }: { id: string; keys: string[] }) => api.saveMultiplierGroups(id, keys),
    onSuccess: (result, variables) => applyResult(result, variables.keys.length ? `已监控 ${variables.keys.length} 个分组，首次倍率已作为基准。` : '已取消当前渠道的全部倍率监控。')
  })

  const clearOperationMessages = () => {
    setSuccessMessage('')
    detectMutation.reset()
    checkMutation.reset()
    saveMutation.reset()
  }

  const viewState = detectedState?.targetId === targetId ? detectedState : stateQuery.data
  const groups = viewState?.groups ?? []
  const monitoredCount = groups.filter((group) => group.monitored).length
  const operationPending = detectMutation.isPending || checkMutation.isPending || saveMutation.isPending
  const operationError = detectMutation.error ?? checkMutation.error ?? saveMutation.error

  const resetTargetState = (nextTargetId: string) => {
    targetIdRef.current = nextTargetId
    setTargetId(nextTargetId)
    setDetectedState(null)
    setSelectedKeys(new Set())
    setDirty(false)
    setSuccessMessage('')
    detectMutation.reset()
    checkMutation.reset()
    saveMutation.reset()
  }

  useEffect(() => {
    if (targetsQuery.isPending) return
    const requestedExists = eligibleTargets.some((target) => target.id === requestedTargetId)
    const nextTargetId = requestedExists ? requestedTargetId : (eligibleTargets[0]?.id ?? '')
    if (nextTargetId === targetId) {
      if (requestedTargetId !== nextTargetId) {
        setParams(nextTargetId ? { target: nextTargetId } : {}, { replace: true })
      }
      return
    }
    if (operationPending) {
      setParams(targetId ? { target: targetId } : {}, { replace: true })
      return
    }
    const switchAlreadyApproved = approvedTargetIdRef.current === nextTargetId
    if (dirty && targetId && !switchAlreadyApproved && !window.confirm('当前分组选择尚未保存，确定切换渠道吗？')) {
      setParams({ target: targetId }, { replace: true })
      return
    }
    approvedTargetIdRef.current = null
    resetTargetState(nextTargetId)
    if (requestedTargetId !== nextTargetId) {
      setParams(nextTargetId ? { target: nextTargetId } : {}, { replace: true })
    }
  }, [dirty, eligibleTargets, operationPending, requestedTargetId, targetId, targetsQuery.isPending])

  const changeTarget = (nextTargetId: string) => {
    if (operationPending) return
    if (dirty && !window.confirm('当前分组选择尚未保存，确定切换渠道吗？')) return
    // 先修改地址栏，再由同步副作用统一清理旧渠道状态，避免两个状态源互相回退。
    approvedTargetIdRef.current = nextTargetId
    setParams(nextTargetId ? { target: nextTargetId } : {}, { replace: true })
  }

  const toggleGroup = (key: string) => {
    const next = new Set(selectedKeys)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    setSelectedKeys(next)
    setDirty(selectionChanged(next, groups))
    setSuccessMessage('')
  }

  const selectAll = (selected: boolean) => {
    const next = selected ? new Set(groups.filter((group) => group.status !== 'missing' || group.monitored).map((group) => group.key)) : new Set<string>()
    setSelectedKeys(next)
    setDirty(selectionChanged(next, groups))
    setSuccessMessage('')
  }

  const saveSelection = () => {
    if (!targetId) return
    if (selectedKeys.size === 0 && monitoredCount > 0 && !window.confirm('确定取消这个渠道的全部倍率监控吗？取消后重新添加会建立新的基准。')) return
    clearOperationMessages()
    saveMutation.mutate({ id: targetId, keys: [...selectedKeys] })
  }

  if (targetsQuery.isPending) return <LoadingView label="正在读取可监控渠道" />
  if (targetsQuery.isError) return <ErrorView message={targetsQuery.error.message} onRetry={() => void targetsQuery.refetch()} />

  return (
    <div className="page-stack multiplier-page">
      <PageHeader title="分组倍率" description="选择已添加的 New API 或 Sub2API 渠道，倍率发生变化时使用现有通知方式提醒。" />

      {eligibleTargets.length === 0 ? (
        <EmptyState title="还没有可监控的渠道" description="先添加 New API 或 Sub2API 渠道并配置登录信息。" action={<Link className="button primary" to="/targets/new">添加渠道</Link>} />
      ) : (
        <>
          <section className="content-section multiplier-control-panel" aria-labelledby="multiplier-channel-title">
            <div className="section-heading-row">
              <div><h2 id="multiplier-channel-title">选择渠道</h2><p>只会读取当前选中渠道，不会扫描其他站点。</p></div>
              {selectedTarget ? <span className="multiplier-kind">{targetKindLabels[selectedTarget.kind]}</span> : null}
            </div>
            <div className="multiplier-channel-actions">
              <label className="field multiplier-channel-select"><span>已经添加的渠道</span><select value={targetId} disabled={operationPending} onChange={(event) => changeTarget(event.target.value)}>{eligibleTargets.map((target) => <option key={target.id} value={target.id}>{target.name}{target.enabled ? '' : '（已暂停）'}</option>)}</select></label>
              <button className="button primary" type="button" disabled={!selectedTarget?.authConfigured || dirty || operationPending} onClick={() => { if (!targetId) return; clearOperationMessages(); detectMutation.mutate(targetId) }}><Search aria-hidden="true" size={18} />自动检测分组</button>
              <button className="button secondary" type="button" disabled={!selectedTarget?.authConfigured || monitoredCount === 0 || dirty || operationPending} onClick={() => { if (!targetId) return; clearOperationMessages(); checkMutation.mutate(targetId) }}><RefreshCw className={checkMutation.isPending ? 'spin' : ''} aria-hidden="true" size={18} />刷新当前渠道</button>
            </div>
            {selectedTarget && !selectedTarget.authConfigured ? <InlineMessage tone="warning">该渠道还没有可用的登录信息。<Link to={`/targets/${selectedTarget.id}/edit`}>去编辑渠道</Link></InlineMessage> : null}
            {selectedTarget && !selectedTarget.enabled ? <InlineMessage tone="warning">该渠道已暂停，后台不会定时检测倍率；仍可在这里手动检测。</InlineMessage> : null}
            {dirty ? <InlineMessage tone="warning">分组选择尚未保存，请先保存或恢复原来的选择再检测倍率。</InlineMessage> : null}
            {viewState?.lastCheckedAt ? <p className="multiplier-last-check">最近检测：<time dateTime={viewState.lastCheckedAt} title={formatDateTime(viewState.lastCheckedAt)}>{formatRelativeTime(viewState.lastCheckedAt)}</time></p> : null}
          </section>

          {operationError ? <InlineMessage tone="danger">{operationError.message}</InlineMessage> : null}
          {successMessage ? <InlineMessage tone="success">{successMessage}</InlineMessage> : null}
          {viewState?.lastError ? <InlineMessage tone="warning">最近一次倍率检测失败：{viewState.lastError}</InlineMessage> : null}

          {targetId && stateQuery.isPending && !detectedState ? <LoadingView label="正在读取倍率监控" /> : null}
          {targetId && stateQuery.isError && !viewState ? <ErrorView message={stateQuery.error.message} onRetry={() => void stateQuery.refetch()} /> : null}
          {targetId && stateQuery.isError && viewState ? <InlineMessage tone="warning">后台状态重新读取失败，当前显示的是上一次结果。<button className="button ghost compact" type="button" onClick={() => void stateQuery.refetch()}>重新读取</button></InlineMessage> : null}

          {viewState && !stateQuery.isPending ? (
            <section className="content-section" aria-labelledby="multiplier-groups-title">
              <div className="section-heading-row multiplier-list-heading">
                <div><h2 id="multiplier-groups-title">分组列表</h2><p>{groups.length ? `已选 ${selectedKeys.size} 个，共显示 ${groups.length} 个分组。` : '点击“自动检测分组”读取这个渠道当前可用的固定倍率分组。'}</p></div>
                {groups.length ? <div className="compact-actions"><button className="button ghost compact" type="button" disabled={operationPending} onClick={() => selectAll(true)}><SquareCheckBig aria-hidden="true" size={17} />全选</button><button className="button ghost compact" type="button" disabled={operationPending} onClick={() => selectAll(false)}>取消全选</button></div> : null}
              </div>

              {groups.length ? (
                <div className="multiplier-table" role="table" aria-label="分组倍率列表">
                  <div className="multiplier-table-header" role="row"><span role="columnheader">监控</span><span role="columnheader">分组</span><span role="columnheader">当前倍率</span><span role="columnheader">上次变化</span><span role="columnheader">状态</span></div>
                  <div className="multiplier-table-body" role="rowgroup">{groups.map((group) => {
                    const displayStatus = group.lastError ? 'unknown' : group.status
                    const StatusIcon = statusIcons[displayStatus]
                    const checked = selectedKeys.has(group.key)
                    return <div className={`multiplier-row status-${displayStatus}`} role="row" key={group.key} id={`multiplier-${group.key}`}>
                      <label className="multiplier-checkbox" role="cell"><input type="checkbox" checked={checked} disabled={operationPending} onChange={() => toggleGroup(group.key)} /><span className="sr-only">监控 {group.name}</span></label>
                      <div className="multiplier-group-name" role="cell"><strong>{group.name}</strong>{group.description ? <small>{group.description}</small> : null}<small>标识：{group.key}</small></div>
                      <div className="multiplier-value" role="cell"><span className="mobile-cell-label">当前倍率</span><strong>{multiplierText(group.multiplier)}</strong>{group.previousMultiplier && group.status === 'changed' ? <small>{group.previousMultiplier}× → {group.multiplier}×</small> : null}</div>
                      <div className="multiplier-changed-at" role="cell"><span className="mobile-cell-label">上次变化</span>{group.changedAt ? <time dateTime={group.changedAt} title={formatDateTime(group.changedAt)}>{formatRelativeTime(group.changedAt)}</time> : <span>暂无变化</span>}</div>
                      <div className={`multiplier-state state-${displayStatus}`} role="cell"><StatusIcon aria-hidden="true" size={18} /><span>{groupStatusLabel(group)}</span>{group.lastError ? <small>{group.lastError}</small> : null}</div>
                    </div>
                  })}</div>
                </div>
              ) : <EmptyState title="尚未检测分组" description="自动检测后可以勾选要监控的分组；第一次保存只建立基准，不会误发通知。" />}

              {groups.length ? <div className="multiplier-save-bar"><div><Percent aria-hidden="true" size={19} /><span>只比较已勾选分组的实际倍率；名称或说明变化不会触发提醒。</span></div><button className="button primary" type="button" disabled={!dirty || operationPending} onClick={saveSelection}><Save aria-hidden="true" size={18} />{saveMutation.isPending ? '正在保存' : '保存监控'}</button></div> : null}
            </section>
          ) : null}
        </>
      )}
    </div>
  )
}
