import type { FC, ReactNode } from "react";

interface Tab {
  id: string;
  label: string;
}

interface SubTabsProps {
  tabs: Tab[];
  activeTab: string;
  onTabChange: (id: string) => void;
  className?: string;
  children?: ReactNode;
}

const SubTabs: FC<SubTabsProps> = ({
  tabs,
  activeTab,
  onTabChange,
  className = "",
  children,
}) => {
  return (
    <div className={`flex items-center border-b border-slate-800 ${className}`}>
      <div className="flex gap-0" role="tablist">
        {tabs.map((tab) => {
          const isActive = tab.id === activeTab;
          return (
            <button
              key={tab.id}
              type="button"
              role="tab"
              aria-selected={isActive}
              onClick={() => onTabChange(tab.id)}
              className={`border-b-2 px-4 py-2 text-sm font-medium transition-colors ${
                isActive
                  ? "border-purple-500 text-white"
                  : "border-transparent text-slate-400 hover:text-slate-200"
              }`}
            >
              {tab.label}
            </button>
          );
        })}
      </div>
      {children && <div className="ml-auto">{children}</div>}
    </div>
  );
};

export default SubTabs;
