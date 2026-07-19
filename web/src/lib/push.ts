import { api } from '../api/client'

function urlBase64ToUint8Array(value: string): Uint8Array<ArrayBuffer> {
  const padding = '='.repeat((4 - (value.length % 4)) % 4)
  const base64 = (value + padding).replace(/-/g, '+').replace(/_/g, '/')
  const raw = window.atob(base64)
  return Uint8Array.from(raw, (character) => character.charCodeAt(0))
}

export function canUsePush(): boolean {
  return 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window
}

export async function enablePush(vapidPublicKey: string, deviceName: string): Promise<void> {
  if (!canUsePush()) throw new Error('当前浏览器不支持系统推送')
  if (!vapidPublicKey) throw new Error('服务器尚未生成推送公钥')

  const permission = await Notification.requestPermission()
  if (permission !== 'granted') throw new Error('通知权限未开启，请在浏览器设置中允许通知')

  const registration = await navigator.serviceWorker.ready
  const existing = await registration.pushManager.getSubscription()
  const subscription = existing ?? await registration.pushManager.subscribe({
    userVisibleOnly: true,
    applicationServerKey: urlBase64ToUint8Array(vapidPublicKey)
  })
  await api.subscribePush({ ...subscription.toJSON(), name: deviceName })
}
