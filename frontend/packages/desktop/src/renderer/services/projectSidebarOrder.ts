import { getClientBusinessSetting, setClientBusinessSetting } from './clientBusinessSettings';
import { normalizeProjectOrder } from '../pages/conversation/GroupedHistory/projectOrderModel';

export const PROJECT_SIDEBAR_ORDER_SETTING = 'synonBiomed.sidebar.projectOrder' as const;

export async function loadProjectSidebarOrder(): Promise<string[]> {
  return normalizeProjectOrder(await getClientBusinessSetting(PROJECT_SIDEBAR_ORDER_SETTING));
}

export async function saveProjectSidebarOrder(projectIds: readonly string[]): Promise<void> {
  await setClientBusinessSetting(PROJECT_SIDEBAR_ORDER_SETTING, normalizeProjectOrder(projectIds));
}
