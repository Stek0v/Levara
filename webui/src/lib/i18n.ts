// Minimal dictionary-based i18n for the WebUI. No external dependency:
// locale lives in backend settings (useSettings), strings are looked up
// in a flat map with en fallback. Format helpers use Intl with the
// active locale.
import { useSettings } from '@/hooks/use-levara'

export type Locale = 'ru' | 'en'

type Dict = Record<string, string>

const ru: Dict = {
  // Navigation / common
  'nav.projects': 'Проекты',
  'nav.chat': 'Чат',
  'nav.memories': 'Воспоминания',
  'nav.tasks': 'Задачи',
  'nav.settings': 'Настройки',
  'nav.group.work': 'Работа',
  'nav.group.memory': 'Память',
  'nav.group.infra': 'Инфраструктура',
  'nav.dashboard': 'Обзор',
  'nav.analytics': 'Аналитика',
  'nav.memoryBehavior': 'Память: поведение',
  'nav.scaffold': 'Предложения памяти',
  'nav.search': 'Поиск',
  'nav.graph': 'Граф',
  'nav.admin': 'Админ',
  'nav.workspace': 'Рабочее пространство',
  'nav.sync': 'Синхронизация',
  'nav.logout': 'Выйти',
  'common.create': 'Создать',
  'common.cancel': 'Отмена',
  'common.delete': 'Удалить',
  'common.save': 'Сохранить',
  'common.clear': 'Очистить',
  'common.name': 'Название',
  'common.status': 'Статус',
  'common.loading': 'Загрузка…',
  'common.empty': 'Пока ничего нет',

  // Projects (datasets)
  'projects.title': 'Проекты',
  'projects.subtitle': 'Проекты — папки с документами и контекстом',
  'projects.create': 'Создать проект',
  'projects.namePlaceholder': 'Название проекта',
  'projects.empty': 'Проектов пока нет — создайте первый',
  'projects.files': 'Файлов',
  'projects.size': 'Размер',
  'projects.created': 'Создан',
  'projects.recentUploads': 'Недавние загрузки',
  'projects.newPerUpload': 'Новый проект из загрузки',
  'projects.clearHistory': 'Очистить историю',
  'projects.open': 'Открыть',

  // Project detail
  'project.files': 'Файлы',
  'project.file': 'Файл',
  'project.path': 'Путь',
  'project.type': 'Тип',
  'project.uploadedAt': 'Загружен',
  'project.status.ready': 'Обработан',
  'project.status.processing': 'В обработке',
  'project.status.error': 'Ошибка',
  'project.status.unknown': '—',
  'project.deleteFile': 'Удалить файл',
  'project.dropzone': 'Перетащите файлы сюда или нажмите для выбора',
  'project.tab.files': 'Файлы',
  'project.tab.context': 'Контекст',
  'project.tab.history': 'История',
  'project.context.empty': 'Контекста пока нет — он наполняется по мере работы с проектом',
  'project.history.empty': 'Событий пока нет',
  'project.history.upload': 'Загрузка файла',
  'project.history.share': 'Выдан доступ',
  'project.history.context': 'Добавлен контекст',
  'project.repo.title': 'Репозиторий',
  'project.repo.placeholder': 'Путь к git-репозиторию на сервере',
  'project.repo.save': 'Сохранить',
  'project.repo.saved': 'Сохранено',
  'project.repo.commits': 'Коммиты',
  'project.repo.empty': 'Репозиторий не привязан',
  'project.shares.title': 'Доступ',
  'project.shares.email': 'Email пользователя',
  'project.shares.role': 'Роль',
  'project.shares.grant': 'Выдать доступ',
  'project.shares.revoke': 'Отозвать',
  'project.shares.empty': 'Проект не расшарен никому',
  'project.shares.role.viewer': 'Наблюдатель',
  'project.shares.role.editor': 'Редактор',
  'project.shares.role.admin': 'Администратор',
  'project.shares.error': 'Ошибка доступа',

  // Upload
  'upload.title': 'Загрузка',
  'upload.button': 'Загрузить файлы',

  // Login
  'login.title': 'Вход в Levara',
  'login.email': 'Email',
  'login.password': 'Пароль',
  'login.submit': 'Войти',
  'login.register': 'Регистрация',
  'login.haveAccount': 'Уже есть аккаунт?',
  'login.noAccount': 'Нет аккаунта?',

  // Chat
  'chat.title': 'Чат',
  'chat.allCollections': 'Все коллекции',
  'chat.askPlaceholder': 'Задайте вопрос…',
  'chat.sources': 'Источники',
  'chat.empty': 'Задайте первый вопрос',

  // Memories
  'memories.title': 'Воспоминания',
  'memories.empty': 'Воспоминаний пока нет',
  'memories.searchPlaceholder': 'Поиск по воспоминаниям',

  // Settings
  'settings.title': 'Настройки',
  'settings.appearance': 'Оформление',
  'settings.language': 'Язык',
  'settings.endpoint': 'Сервер',
  'settings.status': 'Состояние',
  'settings.connected': 'Подключено',
  'settings.about': 'О программе',

  // Tasks
  'tasks.title': 'Задачи',
  'tasks.empty': 'Задач нет',
}

const en: Dict = {
  'nav.projects': 'Projects',
  'nav.group.work': 'Work',
  'nav.group.memory': 'Memory',
  'nav.group.infra': 'Infrastructure',
  'nav.dashboard': 'Dashboard',
  'nav.analytics': 'Analytics',
  'nav.memoryBehavior': 'Memory Behavior',
  'nav.scaffold': 'Scaffold Proposals',
  'projects.title': 'Projects',
  'projects.subtitle': 'Projects are folders with documents and context',
  'projects.create': 'New project',
  'projects.namePlaceholder': 'Project name',
  'projects.empty': 'No projects yet — create the first one',
  'projects.files': 'Files',
  'projects.size': 'Size',
  'projects.created': 'Created',
  'projects.recentUploads': 'Recent uploads',
  'projects.newPerUpload': 'New project per upload',
  'projects.clearHistory': 'Clear history',
  'projects.open': 'Open',
  'project.files': 'Files',
  'project.file': 'File',
  'project.path': 'Path',
  'project.type': 'Type',
  'project.uploadedAt': 'Uploaded',
  'project.status.ready': 'Processed',
  'project.status.processing': 'Processing',
  'project.status.error': 'Error',
  'project.status.unknown': '—',
  'project.deleteFile': 'Delete file',
  'project.dropzone': 'Drag files here or click to choose',
  'project.tab.files': 'Files',
  'project.tab.context': 'Context',
  'project.tab.history': 'History',
  'project.context.empty': 'No context yet — it grows as the project is used',
  'project.history.empty': 'No events yet',
  'project.history.upload': 'File uploaded',
  'project.history.share': 'Access granted',
  'project.history.context': 'Context added',
  'project.repo.title': 'Repository',
  'project.repo.placeholder': 'Path to the git repository on the server',
  'project.repo.save': 'Save',
  'project.repo.saved': 'Saved',
  'project.repo.commits': 'Commits',
  'project.repo.empty': 'No repository bound',
  'project.shares.title': 'Access',
  'project.shares.email': 'User email',
  'project.shares.role': 'Role',
  'project.shares.grant': 'Grant access',
  'project.shares.revoke': 'Revoke',
  'project.shares.empty': 'Not shared with anyone',
  'project.shares.role.viewer': 'Viewer',
  'project.shares.role.editor': 'Editor',
  'project.shares.role.admin': 'Admin',
  'project.shares.error': 'Access error',
  'upload.title': 'Upload',
  'upload.button': 'Upload files',
  'login.title': 'Sign in to Levara',
  'login.email': 'Email',
  'login.password': 'Password',
  'login.submit': 'Sign in',
  'login.register': 'Sign up',
  'login.haveAccount': 'Already have an account?',
  'login.noAccount': 'No account?',
  'chat.title': 'Chat',
  'chat.allCollections': 'All collections',
  'chat.askPlaceholder': 'Ask a question…',
  'chat.sources': 'Sources',
  'chat.empty': 'Ask the first question',
  'memories.title': 'Memories',
  'memories.empty': 'No memories yet',
  'memories.searchPlaceholder': 'Search memories',
  'settings.title': 'Settings',
  'settings.appearance': 'Appearance',
  'settings.language': 'Language',
  'settings.endpoint': 'Endpoint',
  'settings.status': 'Status',
  'settings.connected': 'Connected',
  'settings.about': 'About',
  'tasks.title': 'Tasks',
  'tasks.empty': 'No tasks',
}

const dicts: Record<Locale, Dict> = { ru, en }

// Exported for tests (dictionary parity checks).
export const dictionaries = { ru, en }

export function translate(locale: Locale | undefined, key: string): string {
  if (locale && dicts[locale]?.[key]) return dicts[locale][key]
  if (en[key]) return en[key]
  return key
}

// useT returns a translate function bound to the active locale from
// backend settings. During SSR / before settings load, keys fall back
// to en (or the key itself).
export function useT() {
  const { data: settings } = useSettings()
  const locale = (settings?.locale ?? 'en') as Locale | undefined
  return (key: string) => translate(locale, key)
}

// formatBytes renders a human-readable size with ru/en units.
export function formatBytes(n: number | undefined, locale?: Locale): string {
  const ru = locale !== 'en'
  const b = ru ? 'Б' : 'B'
  const kbU = ru ? 'КБ' : 'KB'
  const mbU = ru ? 'МБ' : 'MB'
  const gbU = ru ? 'ГБ' : 'GB'
  const v = n ?? 0
  if (v < 1024) return `${v} ${b}`
  const kb = v / 1024
  if (kb < 1024) return `${kb.toFixed(1)} ${kbU}`
  const mb = kb / 1024
  if (mb < 1024) return `${mb.toFixed(1)} ${mbU}`
  return `${(mb / 1024).toFixed(1)} ${gbU}`
}

export function formatDate(iso: string | undefined, locale: Locale | undefined): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (isNaN(d.getTime())) return iso
  try {
    return new Intl.DateTimeFormat(locale === 'ru' ? 'ru-RU' : 'en-US', {
      day: 'numeric', month: 'short', year: 'numeric',
    }).format(d)
  } catch {
    return iso
  }
}

export function formatCount(n: number | undefined, locale: Locale | undefined): string {
  try {
    return new Intl.NumberFormat(locale === 'ru' ? 'ru-RU' : 'en-US').format(n ?? 0)
  } catch {
    return String(n ?? 0)
  }
}
