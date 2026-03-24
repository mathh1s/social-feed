const API = '/api/posts';
const POLL_INTERVAL = 5000;
const TIMESTAMP_INTERVAL = 30000;
const MAX_AVATAR_DIM = 128;
const MAX_IMAGE_DIM = 800;
const PAGE_SIZE = 20;
const STORAGE_KEY = 'feed_user';
const TOKENS_KEY = 'feed_tokens';
const EMOJIS = ['👍', '🔥', '❤️', '😂'];

marked.setOptions({ breaks: true, gfm: true });

const { createApp, ref, computed, watch, onMounted, onUnmounted, nextTick } = Vue;

// --- Sound effects ---
const SFX = {
  _ctx: null,
  _getCtx() {
    if (!this._ctx) this._ctx = new (window.AudioContext || window.webkitAudioContext)();
    return this._ctx;
  },
  play(freq, dur, type = 'sine', vol = 0.15) {
    try {
      const ctx = this._getCtx();
      const osc = ctx.createOscillator();
      const gain = ctx.createGain();
      osc.type = type;
      osc.frequency.setValueAtTime(freq, ctx.currentTime);
      gain.gain.setValueAtTime(vol, ctx.currentTime);
      gain.gain.exponentialRampToValueAtTime(0.001, ctx.currentTime + dur);
      osc.connect(gain).connect(ctx.destination);
      osc.start(); osc.stop(ctx.currentTime + dur);
    } catch (e) { /* audio not available */ }
  },
  sent() { this.play(880, 0.12, 'sine', 0.1); setTimeout(() => this.play(1100, 0.1, 'sine', 0.08), 80); },
  newPost() { this.play(660, 0.15, 'triangle', 0.08); },
  react() { this.play(520, 0.08, 'sine', 0.06); },
};

// --- Markdown ---
function renderMd(text) {
  return DOMPurify.sanitize(marked.parse(text), {
    ALLOWED_TAGS: ['p','br','strong','em','code','pre','a','blockquote','ul','ol','li','del','h1','h2','h3','h4','hr'],
    ALLOWED_ATTR: ['href','target','rel'],
    ADD_ATTR: ['target'],
  });
}

// --- Timestamps ---
function computeAgo(iso) {
  const diff = (Date.now() - new Date(iso).getTime()) / 1000;
  if (diff < 10) return 'now';
  if (diff < 60) return Math.floor(diff) + 's';
  if (diff < 3600) return Math.floor(diff / 60) + 'm';
  if (diff < 86400) return Math.floor(diff / 3600) + 'h';
  const d = Math.floor(diff / 86400);
  if (d < 30) return d + 'd';
  return Math.floor(d / 30) + 'mo';
}

// --- Image processing ---
function processImage(file, maxDim, quality, callback) {
  const reader = new FileReader();
  reader.onload = () => {
    const img = new Image();
    img.onload = () => {
      const canvas = document.createElement('canvas');
      let w = img.width, h = img.height;
      if (w > maxDim || h > maxDim) {
        const s = maxDim / Math.max(w, h);
        w = Math.round(w * s);
        h = Math.round(h * s);
      }
      canvas.width = w;
      canvas.height = h;
      canvas.getContext('2d').drawImage(img, 0, 0, w, h);
      callback(canvas.toDataURL('image/jpeg', quality));
    };
    img.src = reader.result;
  };
  reader.readAsDataURL(file);
}

function validateFile(file, maxMB, errorRef) {
  if (file.size > maxMB * 1024 * 1024) {
    errorRef.value = `too large (max ${maxMB}MB)`;
    return false;
  }
  if (!['image/png', 'image/jpeg', 'image/gif', 'image/webp'].includes(file.type)) {
    errorRef.value = 'only png/jpeg/gif/webp';
    return false;
  }
  return true;
}

// --- Delete token storage ---
function loadTokens() {
  try { return JSON.parse(localStorage.getItem(TOKENS_KEY)) || {}; } catch { return {}; }
}
function saveToken(postId, token) {
  const t = loadTokens();
  t[postId] = token;
  try { localStorage.setItem(TOKENS_KEY, JSON.stringify(t)); } catch {}
}
function getToken(postId) {
  return loadTokens()[postId] || '';
}
function removeToken(postId) {
  const t = loadTokens();
  delete t[postId];
  try { localStorage.setItem(TOKENS_KEY, JSON.stringify(t)); } catch {}
}

// --- Vue app ---
createApp({
  setup() {
    const author = ref('');
    const content = ref('');
    const avatarData = ref('');
    const imageData = ref('');
    const error = ref('');
    const sending = ref(false);
    const posts = ref([]);
    const pendingPosts = ref([]);
    const initialLoading = ref(true);
    const loadingMore = ref(false);
    const hasMore = ref(true);
    const toasts = ref([]);
    const sentinel = ref(null);
    const lightboxSrc = ref('');
    const replyingTo = ref(null);
    const searchQuery = ref('');
    const searchResults = ref(null);
    const searching = ref(false);

    let pollTimer, timestampTimer, observer, toastCounter = 0, searchDebounce;

    const canSubmit = computed(() =>
      author.value.trim().length > 0 && content.value.trim().length > 0
    );

    // --- localStorage ---
    function loadUser() {
      try {
        const d = JSON.parse(localStorage.getItem(STORAGE_KEY));
        if (d?.author) author.value = d.author;
        if (d?.avatar) avatarData.value = d.avatar;
      } catch {}
    }
    function saveUser() {
      try {
        localStorage.setItem(STORAGE_KEY, JSON.stringify({
          author: author.value,
          avatar: avatarData.value,
        }));
      } catch {}
    }
    watch(author, saveUser);
    watch(avatarData, saveUser);

    // --- Toasts ---
    function toast(msg, type = 'success') {
      const id = ++toastCounter;
      toasts.value.push({ id, msg, type, leaving: false });
      setTimeout(() => {
        const t = toasts.value.find(x => x.id === id);
        if (t) t.leaving = true;
        setTimeout(() => { toasts.value = toasts.value.filter(x => x.id !== id); }, 300);
      }, 2500);
    }

    function refreshTimestamps() {
      posts.value.forEach(p => { p._ago = computeAgo(p.created_at); });
    }

    function enrichPost(p, isNew = false) {
      p._ago = computeAgo(p.created_at);
      p._new = isNew;
      p._preview = null;
      p._replies = [];
      p._showReplies = false;
      p._loadingReplies = false;
      p._replyText = '';
      p._canDelete = !!getToken(p.id);
      p._reactions = p.reactions || {};
      if (p.preview) {
        try { p._preview = JSON.parse(p.preview); } catch {}
      }
      return p;
    }

    // --- Fetch ---
    async function fetchInitial() {
      try {
        const res = await fetch(`${API}?limit=${PAGE_SIZE}`);
        if (!res.ok) throw new Error();
        const data = await res.json();
        posts.value = data.posts.map(p => enrichPost(p));
        hasMore.value = data.has_more;
      } catch { toast('failed to load posts', 'error'); }
      finally { initialLoading.value = false; }
    }

    async function loadMore() {
      if (loadingMore.value || !hasMore.value || !posts.value.length) return;
      loadingMore.value = true;
      try {
        const res = await fetch(`${API}?before_id=${posts.value.at(-1).id}&limit=${PAGE_SIZE}`);
        if (!res.ok) throw new Error();
        const data = await res.json();
        data.posts.forEach(p => posts.value.push(enrichPost(p)));
        hasMore.value = data.has_more;
      } catch {}
      finally { loadingMore.value = false; }
    }

    // --- Polling ---
    function highestId() {
      return pendingPosts.value[0]?.id || posts.value[0]?.id || 0;
    }

    async function poll() {
      if (searchResults.value) return;
      try {
        const res = await fetch(`${API}/new?after_id=${highestId()}`);
        if (!res.ok) return;
        const data = await res.json();
        if (data.length) {
          data.forEach(p => enrichPost(p, true));
          pendingPosts.value = [...data, ...pendingPosts.value];
          SFX.newPost();
        }
      } catch {}
    }

    function mergePending() {
      posts.value = [...pendingPosts.value, ...posts.value];
      pendingPosts.value = [];
      window.scrollTo({ top: 0, behavior: 'smooth' });
    }

    // --- Submit ---
    async function submit() {
      if (!canSubmit.value || sending.value) return;
      error.value = '';
      sending.value = true;
      try {
        const body = {
          author: author.value.trim(),
          avatar: avatarData.value,
          content: content.value.trim(),
          image: imageData.value,
        };
        if (replyingTo.value) body.parent_id = replyingTo.value.id;

        const res = await fetch(API, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        });
        const data = await res.json();
        if (!res.ok) {
          error.value = data.error || 'something went wrong';
          toast(data.error || 'failed', 'error');
          return;
        }

        if (data.delete_token) {
          saveToken(data.id, data.delete_token);
        }

        if (replyingTo.value) {
          const parent = posts.value.find(p => p.id === replyingTo.value.id);
          if (parent) {
            parent.reply_count++;
            if (parent._showReplies) {
              data._canDelete = !!getToken(data.id);
              data._reactions = data.reactions || {};
              parent._replies.push(data);
            }
          }
          replyingTo.value = null;
          toast('replied');
        } else {
          posts.value.unshift(enrichPost(data, true));
          toast('posted');
        }
        content.value = '';
        imageData.value = '';
        SFX.sent();
      } catch {
        error.value = 'network error';
        toast('network error', 'error');
      } finally {
        sending.value = false;
      }
    }

    // --- Replies ---
    function startReply(post) {
      replyingTo.value = post;
      nextTick(() => {
        const el = document.querySelector('.composer textarea');
        if (el) { el.focus(); el.scrollIntoView({ behavior: 'smooth', block: 'center' }); }
      });
    }

    async function toggleReplies(post) {
      if (post._showReplies) { post._showReplies = false; return; }
      post._showReplies = true;
      post._loadingReplies = true;
      try {
        const res = await fetch(`${API}/replies?post_id=${post.id}`);
        if (!res.ok) throw new Error();
        const replies = await res.json();
        post._replies = replies.map(r => {
          r._canDelete = !!getToken(r.id);
          r._reactions = r.reactions || {};
          return r;
        });
      } catch { toast('failed to load replies', 'error'); }
      finally { post._loadingReplies = false; }
    }

    async function submitReply(post) {
      const text = post._replyText?.trim();
      if (!text || !author.value.trim()) {
        toast('name and reply required', 'error');
        return;
      }
      try {
        const res = await fetch(API, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            parent_id: post.id,
            author: author.value.trim(),
            avatar: avatarData.value,
            content: text,
            image: '',
          }),
        });
        const data = await res.json();
        if (!res.ok) { toast(data.error || 'failed', 'error'); return; }
        if (data.delete_token) saveToken(data.id, data.delete_token);
        data._canDelete = !!getToken(data.id);
        data._reactions = data.reactions || {};
        post._replies.push(data);
        post.reply_count++;
        post._replyText = '';
        SFX.sent();
      } catch { toast('network error', 'error'); }
    }

    // --- Reactions ---
    async function react(post, emoji) {
      try {
        const res = await fetch(`/api/react?post_id=${post.id}`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ emoji }),
        });
        if (!res.ok) return;
        const counts = await res.json();
        post._reactions = counts;
        SFX.react();
      } catch {}
    }

    // --- Delete ---
    async function deletePost(post) {
      const token = getToken(post.id);
      if (!token || !confirm('Delete this post?')) return;
      try {
        const res = await fetch(`/api/delete?post_id=${post.id}`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ token }),
        });
        if (!res.ok) {
          const data = await res.json();
          toast(data.error || 'failed to delete', 'error');
          return;
        }
        removeToken(post.id);
        posts.value = posts.value.filter(p => p.id !== post.id);
        toast('deleted');
      } catch { toast('network error', 'error'); }
    }

    // --- Search ---
    function onSearchInput() {
      clearTimeout(searchDebounce);
      const q = searchQuery.value.trim();
      if (q.length < 2) {
        searchResults.value = null;
        return;
      }
      searchDebounce = setTimeout(async () => {
        searching.value = true;
        try {
          const res = await fetch(`/api/search?q=${encodeURIComponent(q)}`);
          if (!res.ok) throw new Error();
          const data = await res.json();
          searchResults.value = data.map(p => enrichPost(p));
        } catch { toast('search failed', 'error'); }
        finally { searching.value = false; }
      }, 300);
    }

    function clearSearch() {
      searchQuery.value = '';
      searchResults.value = null;
    }

    // --- Avatar / Image handlers ---
    function handleAvatar(e) {
      const f = e.target.files[0];
      if (!f || !validateFile(f, 2, error)) return;
      processImage(f, MAX_AVATAR_DIM, 0.8, d => { avatarData.value = d; error.value = ''; });
      e.target.value = '';
    }

    function clearAvatar() { avatarData.value = ''; }

    function handleImage(e) {
      const f = e.target.files[0];
      if (!f || !validateFile(f, 5, error)) return;
      processImage(f, MAX_IMAGE_DIM, 0.85, d => { imageData.value = d; error.value = ''; });
      e.target.value = '';
    }

    function initials(name) {
      const p = name.split(/\s+/).filter(Boolean);
      return p.length >= 2 ? p[0][0] + p[1][0] : name.slice(0, 2);
    }

    function hasReactions(reactions) {
      return reactions && Object.values(reactions).some(v => v > 0);
    }

    // --- Lifecycle ---
    onMounted(async () => {
      loadUser();
      await fetchInitial();
      await nextTick();
      if (sentinel.value) {
        observer = new IntersectionObserver(
          e => { if (e[0].isIntersecting) loadMore(); },
          { rootMargin: '200px' },
        );
        observer.observe(sentinel.value);
      }
      pollTimer = setInterval(poll, POLL_INTERVAL);
      timestampTimer = setInterval(refreshTimestamps, TIMESTAMP_INTERVAL);
    });

    onUnmounted(() => {
      clearInterval(pollTimer);
      clearInterval(timestampTimer);
      observer?.disconnect();
    });

    return {
      author, content, avatarData, imageData, error, sending,
      posts, pendingPosts, initialLoading, loadingMore, hasMore,
      toasts, sentinel, lightboxSrc, replyingTo, canSubmit,
      searchQuery, searchResults, searching,
      submit, handleAvatar, clearAvatar, handleImage, mergePending,
      initials, renderMd, computeAgo, startReply, toggleReplies, submitReply,
      react, deletePost, onSearchInput, clearSearch, hasReactions,
      EMOJIS,
    };
  },
}).mount('#app');
