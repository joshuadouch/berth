import { createContext, useContext } from 'react';
import type { Account, BrandSettings, PlanUsage, PluginLink, User } from './api';

export interface Me { user: User; account: Account; name: string; version: string; plugins: PluginLink[]; usage: PlanUsage; brand: BrandSettings; impersonator?: string }

const MeContext = createContext<Me | null>(null);
export const MeProvider = MeContext.Provider;

// useMe returns the signed-in user. isAdmin is true for the server's admins.
export function useMe() {
  const me = useContext(MeContext);
  return { me, isAdmin: me?.user.role === 'admin' };
}
