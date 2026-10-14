import {
  Activity,
  Ban,
  BellRing,
  Bot,
  Brain,
  Bug,
  ClipboardList,
  FolderOpen,
  FolderSync,
  LayoutDashboard,
  type LucideIcon,
  MessageSquare,
  Network,
  Plug,
  Radio,
  ScrollText,
  Settings2,
  ShieldAlert,
  Sparkles,
  Target,
  Terminal,
  Wrench,
} from "lucide-react";

export type NavBadge = "new" | "soon";

export interface NavSubItem {
  id: string;
  title: string;
  url: string;
  icon?: LucideIcon;
  badge?: NavBadge;
  disabled?: boolean;
  newTab?: boolean;
}

interface NavItemBase {
  id: string;
  title: string;
  icon?: LucideIcon;
  badge?: NavBadge;
  disabled?: boolean;
  newTab?: boolean;
}

export interface NavMainLinkItem extends NavItemBase {
  url: string;
  subItems?: never;
}

export interface NavMainParentItem extends NavItemBase {
  subItems: NavSubItem[];
}

export type NavMainItem = NavMainLinkItem | NavMainParentItem;

export interface NavGroup {
  id: number;
  label?: string;
  items: NavMainItem[];
}

export const sidebarItems: NavGroup[] = [
  {
    id: 1,
    label: "Chức năng",
    items: [
      { id: "dashboard", title: "Bảng điều khiển", url: "/dashboard", icon: LayoutDashboard },
      { id: "chat", title: "Hội thoại", url: "/chat", icon: MessageSquare },
      { id: "tasks", title: "Tác vụ", url: "/function/tasks", icon: Target },
      { id: "findings", title: "Phát hiện", url: "/function/findings", icon: Bug },
      { id: "traffic", title: "Lưu lượng", url: "/function/traffic", icon: Activity },
      { id: "commands", title: "Thực thi công cụ", url: "/function/commands", icon: Terminal },
      { id: "llm-records", title: "Ghi LLM", url: "/function/llm-records", icon: Radio },
      { id: "assets", title: "Tài sản", url: "/function/assets", icon: Network },
      { id: "sync", title: "Đồng bộ tài sản", url: "/function/sync", icon: FolderSync },
      { id: "workspace", title: "Không gian làm việc", url: "/function/workspace", icon: FolderOpen },
    ],
  },
  {
    id: 2,
    label: "Hệ thống",
    items: [
      { id: "llm", title: "LLM", url: "/system/llm", icon: Brain },
      { id: "agents", title: "Agent", url: "/system/agents", icon: Bot },
      { id: "mcp", title: "MCP", url: "/system/mcp", icon: Plug },
      { id: "skills", title: "Skill", url: "/system/skills", icon: Sparkles },
      { id: "tools", title: "Công cụ", url: "/system/tools", icon: Wrench },
      { id: "notify", title: "Đẩy thông báo", url: "/system/notify", icon: BellRing },
      { id: "intercept", title: "Quy tắc chặn", url: "/system/intercept", icon: ShieldAlert },
      { id: "asset-intercept", title: "Chặn tài sản", url: "/system/intercept/assets", icon: Ban },
      { id: "approvals", title: "Bản ghi phê duyệt", url: "/system/intercept/approvals", icon: ClipboardList },
      { id: "logs", title: "Nhật ký", url: "/system/logs", icon: ScrollText },
      { id: "settings", title: "Cấu hình hệ thống", url: "/system/settings", icon: Settings2 },
    ],
  },
];
