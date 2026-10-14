"use client";

import * as React from "react";

import { LibraryIcon, PlusIcon, SaveIcon, Settings2Icon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { AssetInterceptRulesEditor } from "@/components/asset-intercept-rules-editor";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import {
  Combobox,
  ComboboxContent,
  ComboboxEmpty,
  ComboboxInput,
  ComboboxItem,
  ComboboxList,
} from "@/components/ui/combobox";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { AssetInterceptRuleInput, TaskCategory, TaskTemplate } from "@/lib/types";
import { cn } from "@/lib/utils";

interface TemplateDraft {
  name: string;
  description: string;
  goal: string;
  categoryID: number | null;
  interceptRules: AssetInterceptRuleInput[];
}

// TemplateSeed là giá trị ban đầu được đưa từ form tạo mới khi chọn "Lưu thành mẫu".
type TemplateSeed = Pick<TemplateDraft, "description" | "goal" | "categoryID" | "interceptRules">;

interface TaskTemplateManagerProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  templates: TaskTemplate[];
  seed: TemplateSeed | null;
  onCreated: (template: TaskTemplate) => void;
  onUpdated: (template: TaskTemplate) => void;
  onDeleted: (id: number) => void;
}

const emptyDraft = (): TemplateDraft => ({
  name: "",
  description: "",
  goal: "",
  categoryID: null,
  interceptRules: [],
});

function templateDraft(template: TaskTemplate): TemplateDraft {
  return {
    name: template.name,
    description: template.description,
    goal: template.goal,
    categoryID: template.category_id ?? null,
    interceptRules: template.intercept_rules ?? [],
  };
}

function TaskTemplateManager({
  open,
  onOpenChange,
  templates,
  seed,
  onCreated,
  onUpdated,
  onDeleted,
}: TaskTemplateManagerProps) {
  const [selectedID, setSelectedID] = React.useState<number | null>(null);
  const [draft, setDraft] = React.useState<TemplateDraft>(emptyDraft);
  const [saving, setSaving] = React.useState(false);
  const [deleteOpen, setDeleteOpen] = React.useState(false);
  const [deleting, setDeleting] = React.useState(false);
  const [categories, setCategories] = React.useState<TaskCategory[]>([]);
  const wasOpen = React.useRef(false);

  React.useEffect(() => {
    if (!open) return;
    api
      .taskCategories()
      .then(setCategories)
      .catch(() => setCategories([]));
  }, [open]);

  React.useEffect(() => {
    if (open && !wasOpen.current) {
      if (seed) {
        setSelectedID(null);
        setDraft({
          name: "",
          description: seed.description,
          goal: seed.goal,
          categoryID: seed.categoryID,
          interceptRules: seed.interceptRules,
        });
      } else if (templates[0]) {
        setSelectedID(templates[0].id);
        setDraft(templateDraft(templates[0]));
      } else {
        setSelectedID(null);
        setDraft(emptyDraft());
      }
    }
    wasOpen.current = open;
  }, [open, seed, templates]);

  const selectTemplate = (template: TaskTemplate) => {
    setSelectedID(template.id);
    setDraft(templateDraft(template));
  };

  const startNew = () => {
    setSelectedID(null);
    setDraft(emptyDraft());
  };

  const updateDraft = (field: "name" | "description" | "goal", value: string) => {
    setDraft((current) => ({ ...current, [field]: value }));
  };

  const patchDraft = (patch: Partial<TemplateDraft>) => {
    setDraft((current) => ({ ...current, ...patch }));
  };

  async function save() {
    const input = {
      name: draft.name.trim(),
      description: draft.description.trim(),
      goal: draft.goal.trim(),
      category_id: draft.categoryID,
      intercept_rules: draft.interceptRules
        .map((r) => ({ ...r, pattern: r.pattern.trim() }))
        .filter((r) => r.pattern !== ""),
    };
    if (!input.name || !input.description || !input.goal) {
      toast.error("Vui lòng điền tên mẫu, mô tả và mục tiêu");
      return;
    }
    setSaving(true);
    try {
      if (selectedID == null) {
        const created = await api.createTaskTemplate(input);
        onCreated(created);
        setSelectedID(created.id);
        setDraft(templateDraft(created));
        toast.success("Mẫu đã được tạo");
      } else {
        const updated = await api.updateTaskTemplate(selectedID, input);
        onUpdated(updated);
        setDraft(templateDraft(updated));
        toast.success("Mẫu đã được cập nhật");
      }
    } catch (error) {
      toast.error(`Lưu thất bại: ${(error as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  async function remove() {
    if (selectedID == null) return;
    const deletedID = selectedID;
    setDeleting(true);
    try {
      await api.deleteTaskTemplate(deletedID);
      onDeleted(deletedID);
      const next = templates.find((template) => template.id !== deletedID);
      if (next) {
        setSelectedID(next.id);
        setDraft(templateDraft(next));
      } else {
        startNew();
      }
      setDeleteOpen(false);
      toast.success("Mẫu đã được xóa");
    } catch (error) {
      toast.error(`Xóa thất bại: ${(error as Error).message}`);
    } finally {
      setDeleting(false);
    }
  }

  let saveLabel = saving ? "Đang lưu" : "Lưu thay đổi";
  if (!saving && selectedID == null) saveLabel = "Tạo mẫu";

  return (
    <>
      <Sheet open={open} onOpenChange={onOpenChange}>
        <SheetContent className="grid h-full w-full! max-w-none! grid-rows-[auto_minmax(0,1fr)_auto] gap-0 overflow-hidden p-0 sm:w-[48rem]! sm:max-w-[48rem]!">
          <SheetHeader className="border-b px-6 py-5">
            <SheetTitle>Quản lý mẫu tác vụ</SheetTitle>
            <SheetDescription>Mẫu lưu mô tả, mục tiêu, phân loại và quy tắc chặn/cho phép cấp tác vụ; thay đổi không ảnh hưởng đến các tác vụ đã tạo.</SheetDescription>
          </SheetHeader>
          <div className="grid min-h-0 overflow-y-auto lg:grid-cols-[15rem_minmax(0,1fr)] lg:overflow-hidden">
            <div className="flex min-h-0 flex-col border-b p-3 lg:border-r lg:border-b-0">
              <Button type="button" variant="outline" className="w-full" onClick={startNew}>
                <PlusIcon data-icon="inline-start" />
                Tạo mẫu mới
              </Button>
              <ScrollArea className="mt-2 max-h-44 lg:max-h-none lg:flex-1">
                <div className="flex flex-col gap-1 pr-2">
                  {templates.length === 0 && (
                    <p className="px-2 py-6 text-center text-muted-foreground text-sm">Chưa có mẫu</p>
                  )}
                  {templates.map((template) => (
                    <button
                      key={template.id}
                      type="button"
                      className={cn(
                        "min-w-0 rounded-md px-2.5 py-2 text-left transition-colors",
                        selectedID === template.id ? "bg-accent text-accent-foreground" : "hover:bg-accent/50",
                      )}
                      onClick={() => selectTemplate(template)}
                    >
                      <span className="block truncate font-medium text-sm">{template.name}</span>
                      <span className="block truncate text-muted-foreground text-xs">{template.description}</span>
                    </button>
                  ))}
                </div>
              </ScrollArea>
            </div>
            <ScrollArea className="min-h-0">
              <FieldGroup className="p-6">
                <Field>
                  <FieldLabel htmlFor="task-template-name">Tên mẫu</FieldLabel>
                  <Input
                    id="task-template-name"
                    value={draft.name}
                    maxLength={120}
                    placeholder="Ví dụ: Kiểm thử xâm nhập Web bên ngoài"
                    onChange={(event) => updateDraft("name", event.target.value)}
                  />
                </Field>
                <Field>
                  <FieldLabel htmlFor="task-template-description">Mô tả</FieldLabel>
                  <Textarea
                    id="task-template-description"
                    className="min-h-28"
                    value={draft.description}
                    placeholder="Đối tượng và bối cảnh kiểm thử"
                    onChange={(event) => updateDraft("description", event.target.value)}
                  />
                </Field>
                <Field>
                  <FieldLabel htmlFor="task-template-goal">Mục tiêu</FieldLabel>
                  <Textarea
                    id="task-template-goal"
                    className="min-h-28"
                    value={draft.goal}
                    placeholder="Mục tiêu tác vụ cần đạt được"
                    onChange={(event) => updateDraft("goal", event.target.value)}
                  />
                </Field>
                <Field>
                  <FieldLabel htmlFor="task-template-category">Phân loại tác vụ</FieldLabel>
                  <NativeSelect
                    id="task-template-category"
                    className="w-full"
                    value={draft.categoryID == null ? "" : String(draft.categoryID)}
                    onChange={(event) =>
                      patchDraft({ categoryID: event.target.value === "" ? null : Number(event.target.value) })
                    }
                  >
                    <NativeSelectOption value="">Chưa phân loại</NativeSelectOption>
                    {categories.map((c) => (
                      <NativeSelectOption key={c.id} value={String(c.id)}>
                        {c.name}
                      </NativeSelectOption>
                    ))}
                  </NativeSelect>
                  <FieldDescription>Khi áp dụng mẫu, điền sẵn phân loại này (có thể sửa lại).</FieldDescription>
                </Field>
                <Field>
                  <FieldLabel>Quy tắc chặn / cho phép cấp tác vụ</FieldLabel>
                  <AssetInterceptRulesEditor
                    value={draft.interceptRules}
                    onChange={(rules) => patchDraft({ interceptRules: rules })}
                  />
                  <FieldDescription>
                    Khi áp dụng mẫu, điền sẵn các quy tắc cấp tác vụ này (chặn/cho phép, chỉ có hiệu lực với tác vụ mới, không đưa vào toàn cục).
                  </FieldDescription>
                </Field>
              </FieldGroup>
            </ScrollArea>
          </div>
          <SheetFooter className="border-t px-6 py-4 sm:flex-row sm:items-center">
            {selectedID != null && (
              <Button type="button" variant="destructive" className="sm:mr-auto" onClick={() => setDeleteOpen(true)}>
                <Trash2Icon data-icon="inline-start" />
                Xóa mẫu
              </Button>
            )}
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Đóng
            </Button>
            <Button type="button" disabled={saving} onClick={() => void save()}>
              {saving ? <Spinner data-icon="inline-start" /> : <SaveIcon data-icon="inline-start" />}
              {saveLabel}
            </Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>
      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Xóa mẫu "{draft.name || "Mẫu chưa đặt tên"}"?</AlertDialogTitle>
            <AlertDialogDescription>Các tác vụ đã tạo từ mẫu này sẽ không bị ảnh hưởng.</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleting}>Hủy</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={deleting}
              onClick={(event) => {
                event.preventDefault();
                void remove();
              }}
            >
              {deleting && <Spinner data-icon="inline-start" />}
              {deleting ? "Đang xóa" : "Xóa"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

interface TaskTemplateControlsProps {
  description: string;
  goal: string;
  categoryID?: number;
  interceptRules?: AssetInterceptRuleInput[];
  selectedTemplateID: number | null;
  onSelectedTemplateIDChange: (id: number | null) => void;
  onApply: (template: TaskTemplate) => void;
  portalContainer?: React.RefObject<HTMLElement | null>;
}

export function TaskTemplateControls({
  description,
  goal,
  categoryID,
  interceptRules,
  selectedTemplateID,
  onSelectedTemplateIDChange,
  onApply,
  portalContainer,
}: TaskTemplateControlsProps) {
  const [templates, setTemplates] = React.useState<TaskTemplate[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [templateInputValue, setTemplateInputValue] = React.useState("");
  const [pendingTemplate, setPendingTemplate] = React.useState<TaskTemplate | null>(null);
  const [managerOpen, setManagerOpen] = React.useState(false);
  const [managerSeed, setManagerSeed] = React.useState<TemplateSeed | null>(null);

  const loadTemplates = React.useCallback(async () => {
    setLoading(true);
    try {
      setTemplates(await api.taskTemplates());
    } catch (error) {
      setTemplates([]);
      toast.error(`Tải mẫu thất bại: ${(error as Error).message}`);
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void loadTemplates();
  }, [loadTemplates]);

  const selectedTemplate = React.useMemo(
    () => templates.find((template) => template.id === selectedTemplateID) ?? null,
    [selectedTemplateID, templates],
  );

  React.useEffect(() => {
    setTemplateInputValue(selectedTemplate?.name ?? "");
  }, [selectedTemplate]);

  const applyTemplate = (template: TaskTemplate) => {
    onApply(template);
    onSelectedTemplateIDChange(template.id);
    setPendingTemplate(null);
  };

  const chooseTemplate = (template: TaskTemplate | null) => {
    if (!template) {
      setTemplateInputValue("");
      onSelectedTemplateIDChange(null);
      return;
    }
    setTemplateInputValue(template.name);
    const hasContent = description.trim() !== "" || goal.trim() !== "";
    const changesContent = description !== template.description || goal !== template.goal;
    if (hasContent && changesContent) {
      setPendingTemplate(template);
      return;
    }
    applyTemplate(template);
  };

  const openManager = (seed: TemplateSeed | null) => {
    setManagerSeed(seed);
    setManagerOpen(true);
  };

  const upsertTemplate = (template: TaskTemplate) => {
    setTemplates((current) => [template, ...current.filter((item) => item.id !== template.id)]);
  };

  const deleteTemplate = (id: number) => {
    setTemplates((current) => current.filter((template) => template.id !== id));
    if (selectedTemplateID === id) onSelectedTemplateIDChange(null);
  };

  let pickerPlaceholder = loading ? "Đang tải mẫu" : "Chưa có mẫu tác vụ";
  if (!loading && templates.length > 0) pickerPlaceholder = "Tìm kiếm và chọn mẫu tác vụ";

  return (
    <>
      <Field>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <FieldLabel htmlFor="task-template-picker">Mẫu tác vụ</FieldLabel>
          <div className="flex flex-wrap gap-2">
            <Button type="button" variant="ghost" size="sm" onClick={() => openManager(null)}>
              <Settings2Icon data-icon="inline-start" />
              Quản lý mẫu
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              disabled={!description.trim() || !goal.trim()}
              onClick={() =>
                openManager({
                  description,
                  goal,
                  categoryID: categoryID ?? null,
                  interceptRules: interceptRules ?? [],
                })
              }
            >
              <SaveIcon data-icon="inline-start" />
              Lưu thành mẫu
            </Button>
          </div>
        </div>
        <Combobox
          items={templates}
          itemToStringLabel={(template) => template.name}
          itemToStringValue={(template) => String(template.id)}
          inputValue={templateInputValue}
          onInputValueChange={(value) => setTemplateInputValue(value)}
          value={selectedTemplate}
          onValueChange={chooseTemplate}
        >
          <ComboboxInput
            id="task-template-picker"
            className="w-full"
            placeholder={pickerPlaceholder}
            disabled={loading || templates.length === 0}
            showClear
          />
          <ComboboxContent portalContainer={portalContainer}>
            <ComboboxEmpty>Không có mẫu phù hợp</ComboboxEmpty>
            <ComboboxList>
              {(template) => (
                <ComboboxItem key={template.id} value={template}>
                  <LibraryIcon />
                  <div className="min-w-0 flex-1">
                    <p className="truncate font-medium text-sm">{template.name}</p>
                    {template.description && (
                      <p className="truncate text-muted-foreground text-xs">{template.description}</p>
                    )}
                  </div>
                </ComboboxItem>
              )}
            </ComboboxList>
          </ComboboxContent>
        </Combobox>
        <FieldDescription>Sau khi chọn, mô tả, mục tiêu, phân loại và quy tắc cấp tác vụ của mẫu sẽ được sao chép, không giữ liên kết với mẫu.</FieldDescription>
      </Field>

      <AlertDialog
        open={pendingTemplate != null}
        onOpenChange={(open) => {
          if (open) return;
          setPendingTemplate(null);
          setTemplateInputValue(selectedTemplate?.name ?? "");
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Dùng mẫu "{pendingTemplate?.name}"?</AlertDialogTitle>
            <AlertDialogDescription>Mô tả và mục tiêu đã điền hiện tại sẽ bị nội dung mẫu ghi đè.</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Hủy</AlertDialogCancel>
            <AlertDialogAction onClick={() => pendingTemplate && applyTemplate(pendingTemplate)}>
              Ghi đè và sử dụng
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <TaskTemplateManager
        open={managerOpen}
        onOpenChange={setManagerOpen}
        templates={templates}
        seed={managerSeed}
        onCreated={upsertTemplate}
        onUpdated={upsertTemplate}
        onDeleted={deleteTemplate}
      />
    </>
  );
}
