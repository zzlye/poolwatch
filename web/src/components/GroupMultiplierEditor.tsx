import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertCircle, BellRing, CheckCircle2, CircleOff, Clock3, Percent, RefreshCw, Save, Search, SquareCheckBig } from 'lucide-react'
import { Link } from 'react-router-dom'
import { api } from '../api/client'
import { formatDateTime, formatRelativeTime } from '../lib/format'
import type { GroupMultiplier, MultiplierStatus, Target, TargetMultiplierState } from '../types'
import { EmptyState, ErrorView, InlineMessage, LoadingView } from './Common'

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

function monitoredState(state: TargetMultiplierState): TargetMultiplierState {
  // 规范缓存只保存已经监控的分组，自动检测出的候选分组始终留在编辑器本地。
  return { ...state, groups: state.groups.filter((group) => group.monitored) }
}

function mergeStoredGroups(candidate: TargetMultiplierState, stored: TargetMultiplierState): TargetMultiplierState {
  const storedByKey = new Map(stored.groups.map((group) => [group.key, group]))
  const groups = candidate.groups.map((group) => {
    const latest = storedByKey.get(group.key)
    if (latest) {
      storedByKey.delete(group.key)
      return latest
    }
    return group.monitored ? { ...group, monitored: false, status: 'unknown' as const, lastError: undefined } : group
  })
  groups.push(...storedByKey.values())
  return { ...candidate, ...stored, groups }
}

export function GroupMultiplierEditor({ target }: { target: Target }) {
  const queryClient = useQueryClient()
  const [candidateState, setCandidateState] = useState<TargetMultiplierState | null>(null)
  const [selectedKeys, setSelectedKeys] = useState<Set<string>>(new Set())
  const [dirty, setDirty] = useState(false)
  const [successMessage, setSuccessMessage] = useState('')
  const stateQuery = useQuery({
    queryKey: ['group-multipliers', target.id],
    queryFn: () => api.multiplierState(target.id)
  })

  useEffect(() => {
    const stored = stateQuery.data
    if (!stored || dirty) return
    setCandidateState((current) => current ? mergeStoredGroups(current, stored) : current)
    setSelectedKeys(new Set(stored.groups.filter((group) => group.monitored).map((group) => group.key)))
  }, [dirty, stateQuery.dataUpdatedAt, target.id])

  const cacheStoredState = (result: TargetMultiplierState) => {
    queryClient.setQueryData(['group-multipliers', target.id], monitoredState(result))
    void queryClient.invalidateQueries({ queryKey: ['group-prices', target.id] })
    void queryClient.invalidateQueries({ queryKey: ['alerts'] })
    void queryClient.invalidateQueries({ queryKey: ['dashboard'] })
  }

  const applyLocalResult = (result: TargetMultiplierState, message: string, updateStoredCache: boolean) => {
    if (updateStoredCache) cacheStoredState(result)
    setCandidateState(result)
    setSelectedKeys(new Set(result.groups.filter((group) => group.monitored).map((group) => group.key)))
    setDirty(false)
    setSuccessMessage(message)
  }

  const detectMutation = useMutation({
    mutationFn: () => api.detectMultiplierGroups(target.id),
    onSuccess: (result) => applyLocalResult(result, `已检测到 ${result.groups.length} 个固定倍率分组。`, true)
  })
  const checkMutation = useMutation({
    mutationFn: () => api.checkMultiplierGroups(target.id),
    onSuccess: (result) => {
      applyLocalResult(result, '当前渠道倍率已经刷新。', true)
    }
  })
  const saveMutation = useMutation({
    mutationFn: (keys: string[]) => api.saveMultiplierGroups(target.id, keys),
    onSuccess: (result, keys) => applyLocalResult(result, keys.length ? `已监控 ${keys.length} 个分组，首次倍率已作为基准。` : '已取消当前渠道的全部倍率监控。', true)
  })

  const clearOperationMessages = () => {
    setSuccessMessage('')
    detectMutation.reset()
    checkMutation.reset()
    saveMutation.reset()
  }

  const viewState = candidateState ?? stateQuery.data
  const groups = viewState?.groups ?? []
  const monitoredCount = groups.filter((group) => group.monitored).length
  const operationPending = detectMutation.isPending || checkMutation.isPending || saveMutation.isPending
  const operationError = detectMutation.error ?? checkMutation.error ?? saveMutation.error

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
    if (selectedKeys.size === 0 && monitoredCount > 0 && !window.confirm('确定取消这个渠道的全部倍率监控吗？取消后重新添加会建立新的基准。')) return
    clearOperationMessages()
    saveMutation.mutate([...selectedKeys])
  }

  return (
    <section className="content-section multiplier-editor" id="multiplier-settings" aria-labelledby="multiplier-settings-title">
      <div className="section-heading-row multiplier-list-heading">
        <div><h2 id="multiplier-settings-title">分组倍率监控</h2><p>自动读取当前渠道分组，只有勾选并保存的分组会在倍率变化时提醒。</p></div>
        <div className="compact-actions">
          <button className="button secondary compact" type="button" disabled={!target.authConfigured || dirty || operationPending} onClick={() => { clearOperationMessages(); detectMutation.mutate() }}><Search aria-hidden="true" size={17} />自动检测分组</button>
          <button className="button ghost compact" type="button" disabled={!target.authConfigured || monitoredCount === 0 || dirty || operationPending} onClick={() => { clearOperationMessages(); checkMutation.mutate() }}><RefreshCw className={checkMutation.isPending ? 'spin' : ''} aria-hidden="true" size={17} />刷新倍率</button>
        </div>
      </div>

      {!target.authConfigured ? <InlineMessage tone="warning">该渠道还没有可用的登录信息。<Link to={`/targets/${target.id}/edit`}>去编辑登录信息</Link></InlineMessage> : null}
      {!target.enabled ? <InlineMessage tone="warning">该渠道已暂停，后台不会定时检测倍率；仍可在这里手动检测。</InlineMessage> : null}
      {dirty ? <InlineMessage tone="warning">分组选择尚未保存，请先保存或恢复原来的选择再检测倍率。</InlineMessage> : null}
      {operationError ? <InlineMessage tone="danger">{operationError.message}</InlineMessage> : null}
      {successMessage ? <InlineMessage tone="success">{successMessage}</InlineMessage> : null}
      {viewState?.lastError ? <InlineMessage tone="warning">最近一次倍率检测失败：{viewState.lastError}</InlineMessage> : null}
      {stateQuery.isError && viewState ? <InlineMessage tone="warning">后台状态重新读取失败，当前显示的是上一次结果。<button className="button ghost compact" type="button" onClick={() => void stateQuery.refetch()}>重新读取</button></InlineMessage> : null}
      {viewState?.lastCheckedAt ? <p className="multiplier-last-check">最近检测：<time dateTime={viewState.lastCheckedAt} title={formatDateTime(viewState.lastCheckedAt)}>{formatRelativeTime(viewState.lastCheckedAt)}</time></p> : null}

      {stateQuery.isPending && !candidateState ? <LoadingView label="正在读取倍率监控" /> : null}
      {stateQuery.isError && !viewState ? <ErrorView message={stateQuery.error.message} onRetry={() => void stateQuery.refetch()} /> : null}

      {viewState && !stateQuery.isPending ? <>
        <div className="section-heading-row multiplier-editor-groups-heading">
          <p>{groups.length ? `已选 ${selectedKeys.size} 个，共显示 ${groups.length} 个分组。` : '点击“自动检测分组”读取这个渠道当前可用的固定倍率分组。'}</p>
          {groups.length ? <div className="compact-actions"><button className="button ghost compact" type="button" disabled={operationPending} onClick={() => selectAll(true)}><SquareCheckBig aria-hidden="true" size={17} />全选</button><button className="button ghost compact" type="button" disabled={operationPending} onClick={() => selectAll(false)}>取消全选</button></div> : null}
        </div>
        {groups.length ? (
          <div className="multiplier-table" role="table" aria-label="可配置分组倍率列表">
            <div className="multiplier-table-header" role="row"><span role="columnheader">监控</span><span role="columnheader">分组</span><span role="columnheader">当前倍率</span><span role="columnheader">上次变化</span><span role="columnheader">状态</span></div>
            <div className="multiplier-table-body" role="rowgroup">{groups.map((group) => {
              const displayStatus = group.lastError ? 'error' : group.status
              const StatusIcon = group.lastError ? AlertCircle : statusIcons[group.status]
              return <div className={`multiplier-row status-${displayStatus}`} role="row" key={group.key}>
                <label className="multiplier-checkbox" role="cell"><input type="checkbox" checked={selectedKeys.has(group.key)} disabled={operationPending} onChange={() => toggleGroup(group.key)} /><span className="sr-only">监控 {group.name}</span></label>
                <div className="multiplier-group-name" role="cell"><strong>{group.name}</strong>{group.description ? <small>{group.description}</small> : null}<small>标识：{group.key}</small></div>
                <div className="multiplier-value" role="cell"><span className="mobile-cell-label">当前倍率</span><strong>{multiplierText(group.multiplier)}</strong>{group.previousMultiplier && group.status === 'changed' ? <small>{group.previousMultiplier}× → {group.multiplier}×</small> : null}</div>
                <div className="multiplier-changed-at" role="cell"><span className="mobile-cell-label">上次变化</span>{group.changedAt ? <time dateTime={group.changedAt} title={formatDateTime(group.changedAt)}>{formatRelativeTime(group.changedAt)}</time> : <span>暂无变化</span>}</div>
                <div className={`multiplier-state state-${displayStatus}`} role="cell"><StatusIcon aria-hidden="true" size={18} /><span>{groupStatusLabel(group)}</span>{group.lastError ? <small>{group.lastError}</small> : null}</div>
              </div>
            })}</div>
          </div>
        ) : <EmptyState title="尚未检测分组" description="自动检测后可以勾选要监控的分组；第一次保存只建立基准，不会误发通知。" />}
        {groups.length ? <div className="multiplier-save-bar"><div><Percent aria-hidden="true" size={19} /><span>只比较已勾选分组的实际倍率；名称或说明变化不会触发提醒。</span></div><button className="button primary" type="button" disabled={!dirty || operationPending} onClick={saveSelection}><Save aria-hidden="true" size={18} />{saveMutation.isPending ? '正在保存' : '保存监控'}</button></div> : null}
      </> : null}
    </section>
  )
}
