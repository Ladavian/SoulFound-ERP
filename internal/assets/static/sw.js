/* ERP · Service Worker
   策略：
   - /static/ 静态资源：stale-while-revalidate（先用缓存秒开，后台悄悄更新）
   - 页面导航：始终走网络（业务数据必须实时），离线时回退到离线提示页
   - 非 GET 请求（表单 / HTMX 提交）：完全不拦截，交给页面层的重试逻辑

   构建版本由服务端注入：缓存名随之变化，升级后旧缓存会自动被丢弃。
*/
const VERSION = '__ERP_VERSION__';
const STATIC_CACHE = 'erp-static-' + VERSION;
const OFFLINE_URL = '/static/offline.html';

const PRECACHE = [
  OFFLINE_URL,
  '/static/icons/icon-192.png',
  '/static/icons/icon-512.png',
  '/static/icons/apple-touch-icon.png',
  '/static/icons/logo-light.png',
  '/static/icons/logo-dark.png'
];

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(STATIC_CACHE)
      .then((cache) => cache.addAll(PRECACHE))
      .catch(() => undefined)
      .then(() => self.skipWaiting())
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(
        keys.filter((k) => k.startsWith('erp-') && k !== STATIC_CACHE)
            .map((k) => caches.delete(k))
      ))
      .then(() => self.clients.claim())
  );
});

self.addEventListener('message', (event) => {
  if (event.data === 'skipWaiting') self.skipWaiting();
});

self.addEventListener('fetch', (event) => {
  const req = event.request;

  if (req.method !== 'GET') return;

  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;

  // Service Worker 自身不经过缓存，保证更新能被检测到
  if (url.pathname === '/sw.js') return;

  // 静态资源：先用缓存，同时在后台更新
  if (url.pathname.startsWith('/static/')) {
    event.respondWith(
      caches.open(STATIC_CACHE).then((cache) =>
        cache.match(req).then((cached) => {
          const network = fetch(req).then((res) => {
            if (res && res.status === 200 && res.type === 'basic') {
              cache.put(req, res.clone());
            }
            return res;
          }).catch(() => cached);
          return cached || network;
        })
      )
    );
    return;
  }

  // 页面导航：网络优先，失败回退离线提示页
  if (req.mode === 'navigate') {
    event.respondWith(
      fetch(req).catch(() =>
        caches.match(OFFLINE_URL).then((cached) => cached || new Response(
          '当前网络不可用，请恢复网络后重试。',
          { status: 503, headers: { 'Content-Type': 'text/plain; charset=utf-8' } }
        ))
      )
    );
    return;
  }

  // 其他同源 GET（HTMX 局部请求等）：只走网络，不缓存
  event.respondWith(fetch(req).catch(() => new Response('', { status: 503 })));
});
