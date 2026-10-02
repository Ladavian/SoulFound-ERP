/* 加拿大冰酒 ERP · Service Worker
   策略：
   - /static/ 静态资源：缓存优先（带版本号，升级时整体替换）
   - 页面导航：始终走网络（数据必须实时），离线时回退到离线提示页
   - 非 GET 请求（表单/HTMX 提交）：完全不拦截，由页面层的重试逻辑处理
*/
const VERSION = 'v1';
const STATIC_CACHE = 'erp-static-' + VERSION;
const OFFLINE_URL = '/static/offline.html';

const PRECACHE = [
  '/static/css/app.css',
  '/static/js/app.js',
  '/static/vendor/htmx.min.js',
  '/static/vendor/alpine.min.js',
  '/static/icons/icon-192.png',
  '/static/icons/icon-512.png',
  '/static/icons/apple-touch-icon.png',
  OFFLINE_URL
];

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(STATIC_CACHE)
      .then((cache) => cache.addAll(PRECACHE))
      .then(() => self.skipWaiting())
      .catch(() => self.skipWaiting())
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(
        keys
          .filter((k) => k.startsWith('erp-') && k !== STATIC_CACHE)
          .map((k) => caches.delete(k))
      )
    ).then(() => self.clients.claim())
  );
});

self.addEventListener('message', (event) => {
  if (event.data === 'skipWaiting') self.skipWaiting();
});

self.addEventListener('fetch', (event) => {
  const req = event.request;

  // 只处理同源 GET
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;

  // 静态资源：缓存优先
  if (url.pathname.startsWith('/static/')) {
    if (url.pathname.endsWith('/sw.js')) return;
    event.respondWith(
      caches.match(req).then((cached) => {
        if (cached) return cached;
        return fetch(req).then((res) => {
          if (res && res.status === 200 && res.type === 'basic') {
            const copy = res.clone();
            caches.open(STATIC_CACHE).then((cache) => cache.put(req, copy));
          }
          return res;
        });
      })
    );
    return;
  }

  // 页面导航：网络优先，失败回退离线页
  if (req.mode === 'navigate') {
    event.respondWith(
      fetch(req).catch(() =>
        caches.match(OFFLINE_URL).then(
          (cached) =>
            cached ||
            new Response('当前网络不可用，请恢复网络后重试。', {
              status: 503,
              headers: { 'Content-Type': 'text/plain; charset=utf-8' }
            })
        )
      )
    );
    return;
  }

  // HTMX 局部请求：网络优先，不缓存
  event.respondWith(fetch(req).catch(() => new Response('', { status: 503 })));
});
