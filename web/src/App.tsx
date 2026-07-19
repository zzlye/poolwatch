import { lazy, Suspense } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { api } from './api/client'
import { AppShell } from './components/AppShell'
import { ErrorView, LoadingView } from './components/Common'
import { UpdatePrompt } from './components/UpdatePrompt'
import { useRealtime } from './hooks/useRealtime'

const AuthPage = lazy(() => import('./pages/AuthPage'))
const DashboardPage = lazy(() => import('./pages/DashboardPage'))
const TargetsPage = lazy(() => import('./pages/TargetsPage'))
const TargetWizardPage = lazy(() => import('./pages/TargetWizardPage'))
const TargetDetailPage = lazy(() => import('./pages/TargetDetailPage'))
const AlertsPage = lazy(() => import('./pages/AlertsPage'))
const SettingsPage = lazy(() => import('./pages/SettingsPage'))

function ApplicationRoutes() {
  const queryClient = useQueryClient()
  const bootstrapQuery = useQuery({ queryKey: ['bootstrap'], queryFn: api.bootstrap, retry: false })
  const logoutMutation = useMutation({
    mutationFn: api.logout,
    onSettled: async () => {
      queryClient.clear()
      await queryClient.invalidateQueries({ queryKey: ['bootstrap'] })
    }
  })
  useRealtime(Boolean(bootstrapQuery.data?.authenticated))

  if (bootstrapQuery.isPending) return <LoadingView label="正在连接号池监控服务器" />
  if (bootstrapQuery.isError) return <ErrorView message={bootstrapQuery.error.message} onRetry={() => void bootstrapQuery.refetch()} />
  const bootstrap = bootstrapQuery.data

  if (!bootstrap.initialized || !bootstrap.authenticated) {
    return (
      <Suspense fallback={<LoadingView />}>
        <AuthPage
          initialized={bootstrap.initialized}
          productName={bootstrap.productName || '号池监控'}
          onAuthenticated={async () => { await queryClient.invalidateQueries({ queryKey: ['bootstrap'] }) }}
        />
      </Suspense>
    )
  }

  return (
    <Suspense fallback={<LoadingView />}>
      <Routes>
        <Route element={<AppShell bootstrap={bootstrap} onLogout={() => logoutMutation.mutate()} />}>
          <Route index element={<DashboardPage />} />
          <Route path="targets" element={<TargetsPage />} />
          <Route path="targets/new" element={<TargetWizardPage />} />
          <Route path="targets/:id" element={<TargetDetailPage />} />
          <Route path="targets/:id/edit" element={<TargetWizardPage />} />
          <Route path="alerts" element={<AlertsPage />} />
          <Route path="settings" element={<SettingsPage />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Route>
      </Routes>
    </Suspense>
  )
}

export default function App() {
  return (
    <BrowserRouter>
      <ApplicationRoutes />
      <UpdatePrompt />
    </BrowserRouter>
  )
}
