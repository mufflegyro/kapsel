export function videoHref(id) {
  return `/videos/${encodeURIComponent(id)}`;
}

export function channelHref(channel) {
  return channel?.id ? `/channels/${encodeURIComponent(channel.id)}` : '/';
}

export function channelInitial(name) {
  return (name || 'K').trim().slice(0, 1).toUpperCase();
}

export function thumbnailFallback(item) {
  return item?.thumbnail_fallback || channelInitial(item?.title || item?.id || 'Kapsel');
}

export function thumbnailStyle(item) {
  const key = item?.id || item?.owner_id || item?.title || 'kapsel';
  return `--fallback-hue: ${hashString(key) % 360}`;
}

export function isCatalogOnly(item) {
  return item?.archive_state === 'catalog-only';
}

export function videoTileLabel(item) {
  return isCatalogOnly(item) ? `Open metadata for ${item.title}` : `Watch ${item.title}`;
}

export function formatDuration(seconds) {
  const total = Number(seconds) || 0;
  if (total <= 0) return '';
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const remainingSeconds = total % 60;
  if (hours > 0) return `${hours}:${String(minutes).padStart(2, '0')}:${String(remainingSeconds).padStart(2, '0')}`;
  return `${minutes}:${String(remainingSeconds).padStart(2, '0')}`;
}

export function formatDate(value) {
  if (!value) return 'Unpublished';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' }).format(date);
}

export function formatViewCount(count) {
  const value = Number(count) || 0;
  if (value <= 0) return '';
  return `${new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 }).format(value)} views`;
}

export function metadataLine(item) {
  const line = feedMetadataLine(item);
  if (line) return line;
  return isCatalogOnly(item) ? 'Metadata archived - media not downloaded' : 'Media downloaded locally';
}

export function feedMetadataLine(item) {
  const parts = [];
  if (item?.published_at) parts.push(formatDate(item.published_at));
  else if (item?.archived_at) parts.push(`Archived ${formatDate(item.archived_at)}`);
  if (formatViewCount(item?.view_count)) parts.push(formatViewCount(item.view_count));
  if (item?.progress?.watched || item?.watched) parts.push('Watched');
  return parts.join(' · ');
}

export function richTextBlocks(value) {
  const lines = String(value || '').replaceAll('\r\n', '\n').replaceAll('\r', '\n').split('\n');
  const blocks = [];
  let paragraph = [];
  let listItems = [];

  function flushParagraph() {
    if (paragraph.length === 0) return;
    blocks.push({ type: 'paragraph', lines: paragraph.map(linkTokens) });
    paragraph = [];
  }

  function flushList() {
    if (listItems.length === 0) return;
    blocks.push({ type: 'list', items: listItems.map(linkTokens) });
    listItems = [];
  }

  for (const rawLine of lines) {
    const line = rawLine.trimEnd();
    if (line.trim() === '') {
      flushParagraph();
      flushList();
      continue;
    }

    const heading = /^(#{1,3})\s+(.+?)\s*#*$/.exec(line.trim());
    if (heading) {
      flushParagraph();
      flushList();
      blocks.push({ type: 'heading', text: heading[2].trim() });
      continue;
    }

    const listItem = /^[-*]\s+(.+)$/.exec(line.trim());
    if (listItem) {
      flushParagraph();
      listItems.push(listItem[1]);
      continue;
    }

    flushList();
    paragraph.push(line);
  }

  flushParagraph();
  flushList();

  return blocks.length > 0 ? blocks : [{ type: 'paragraph', lines: [[{ text: '' }]] }];
}

function linkTokens(value) {
  const tokens = [];
  const pattern = /\[([^\]\n]+)\]\((https?:\/\/[^\s)]+|mailto:[^\s)]+)\)|(https?:\/\/[^\s<]+)|([A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,})|(\b\d{1,3}:\d{2}(?::\d{2})?\b)/gi;
  let index = 0;
  for (const match of value.matchAll(pattern)) {
    if (match.index > index) tokens.push({ text: value.slice(index, match.index) });
    if (match[1] && match[2]) {
      tokens.push({ text: match[1], href: cleanLinkHref(match[2]) });
    } else if (match[3]) {
      const trimmed = trimTrailingPunctuation(match[3]);
      tokens.push({ text: trimmed.value, href: cleanLinkHref(trimmed.value) });
      if (trimmed.trailing) tokens.push({ text: trimmed.trailing });
    } else if (match[4]) {
      tokens.push({ text: match[4], href: `mailto:${match[4]}` });
    } else if (match[5]) {
      const seconds = timestampSeconds(match[5]);
      tokens.push(seconds === null ? { text: match[5] } : { text: match[5], seconds });
    }
    index = match.index + match[0].length;
  }
  if (index < value.length) tokens.push({ text: value.slice(index) });

  return tokens.filter(token => token.text !== '');
}

function timestampSeconds(value) {
  const parts = String(value).split(':').map(part => Number(part));
  if (parts.some(part => !Number.isInteger(part) || part < 0)) return null;
  if (parts.length === 2) {
    const [minutes, seconds] = parts;
    if (seconds > 59) return null;
    return minutes * 60 + seconds;
  }
  if (parts.length === 3) {
    const [hours, minutes, seconds] = parts;
    if (minutes > 59 || seconds > 59) return null;
    return hours * 3600 + minutes * 60 + seconds;
  }

  return null;
}

function trimTrailingPunctuation(value) {
  let trimmed = value;
  let trailing = '';
  while (trimmed.length > 0) {
    const last = trimmed.at(-1);
    if (/[.,;:!?]/.test(last) || isUnbalancedClosingDelimiter(trimmed, last)) {
      trailing = last + trailing;
      trimmed = trimmed.slice(0, -1);
      continue;
    }
    break;
  }

  return { value: trimmed, trailing };
}

function isUnbalancedClosingDelimiter(value, last) {
  const pairs = { ')': '(', ']': '[', '}': '{' };
  const opener = pairs[last];
  if (!opener) return false;

  return countChars(value, last) > countChars(value, opener);
}

function countChars(value, target) {
  let count = 0;
  for (const char of value) {
    if (char === target) count += 1;
  }

  return count;
}

function cleanLinkHref(value) {
  try {
    const parsed = new URL(value);
    if (parsed.protocol === 'http:' || parsed.protocol === 'https:' || parsed.protocol === 'mailto:') return parsed.href;
  } catch {
    return '';
  }

  return '';
}

function hashString(value) {
  let hash = 0;
  for (const char of String(value)) hash = (hash * 31 + char.charCodeAt(0)) >>> 0;
  return hash;
}
